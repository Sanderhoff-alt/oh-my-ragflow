package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	// ProviderOpenAIAPICompatible matches factory.go:58 for native OpenAI compatible driver.
	ProviderOpenAIAPICompatible = "OpenAI-API-Compatible"
	// PlatformInstanceName identifies platform-managed instances.
	PlatformInstanceName = "platform"
	// DefaultChatModelName is the primary default chat model.
	DefaultChatModelName = "gpt-4o-mini"
	// DefaultEmbeddingModelName is the primary default embedding model.
	DefaultEmbeddingModelName = "text-embedding-3-small"
	// DefaultGatewayBaseURL is the internal loopback proxy endpoint.
	DefaultGatewayBaseURL = "http://127.0.0.1:4000"
)

// GatewayKeyClient defines the contract for LiteLLM virtual key management.
type GatewayKeyClient interface {
	GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error)
	DeleteKey(ctx context.Context, keys []string) (*gateway.DeleteKeyResponse, error)
}

// TenantReconciler handles idempotent provisioning of tenant models and virtual keys.
type TenantReconciler struct {
	gatewayClient   GatewayKeyClient
	reconcileDAO    *dao.ReconcileDAO
	defaultsManager *DefaultsManager
	baseURL         string
	allowedModels   []string
}

// NewTenantReconciler creates a new TenantReconciler.
func NewTenantReconciler(client GatewayKeyClient, baseURL string, allowedModels []string) *TenantReconciler {
	if baseURL == "" {
		baseURL = DefaultGatewayBaseURL
	}
	if len(allowedModels) == 0 {
		allowedModels = []string{
			"platform/chat",
			"platform/embedding",
			DefaultChatModelName,
			DefaultEmbeddingModelName,
		}
	}
	return &TenantReconciler{
		gatewayClient:   client,
		reconcileDAO:    dao.NewReconcileDAO(),
		defaultsManager: NewDefaultsManager(),
		baseURL:         baseURL,
		allowedModels:   allowedModels,
	}
}

// ReconcileOption configures options for Reconcile.
type ReconcileOption func(*reconcileOptions)

type reconcileOptions struct {
	enforceDeadline bool
}

// WithEnforceDeadline requires that a degraded record's next_retry_at is due (<= now)
// when claiming for reconciliation, rejecting future deadlines atomically.
func WithEnforceDeadline() ReconcileOption {
	return func(o *reconcileOptions) {
		o.enforceDeadline = true
	}
}

// Reconcile provisions or updates a tenant's model platform access idempotently.
// If virtual key generation succeeds but database persistence fails, it executes
// a compensating rollback to revoke the key and prevent orphaned credentials.
func (r *TenantReconciler) Reconcile(ctx context.Context, db *gorm.DB, tenantID string, opts ...ReconcileOption) error {
	var optCfg reconcileOptions
	for _, opt := range opts {
		opt(&optCfg)
	}

	if db == nil {
		return dao.ErrNilTransaction
	}
	if tenantID == "" {
		return dao.ErrEmptyTenantID
	}

	silentDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	// 1. Fetch or initialize reconcile record
	rec, err := r.reconcileDAO.GetByTenantID(ctx, silentDB, tenantID)
	if err != nil {
		if errors.Is(err, entity.ErrRecordNotFound) {
			newRec := &entity.TenantReconcileRecord{
				TenantID:   tenantID,
				Status:     entity.StatusPending,
				RetryCount: 0,
				MaxRetries: 50,
				Version:    1,
			}
			if err := silentDB.WithContext(ctx).Create(newRec).Error; err != nil {
				// Concurrent worker may have created it just now: re-fetch!
				rec, err = r.reconcileDAO.GetByTenantID(ctx, silentDB, tenantID)
				if err != nil {
					return fmt.Errorf("failed to initialize reconcile record: %w", err)
				}
			} else {
				rec = newRec
			}
		} else {
			return err
		}
	}

	// 2. Terminal, in-flight, or already converged state guard
	if rec.Status == entity.StatusDeleted || rec.Status == entity.StatusProvisioning || rec.Status == entity.StatusReady || rec.Status == entity.StatusFailed {
		return nil
	}

	// 2.1 Enforce retry deadline if requested by caller (Tier-2 poller)
	if optCfg.enforceDeadline && rec.Status == entity.StatusDegraded && rec.NextRetryAt != nil && rec.NextRetryAt.After(time.Now()) {
		return nil
	}

	// 2.2 Enforce maximum retry threshold
	if rec.MaxRetries > 0 && rec.RetryCount >= rec.MaxRetries {
		_, _ = r.reconcileDAO.MarkFailed(ctx, silentDB, tenantID, rec.Version, "maximum self-healing retries exceeded")
		return nil
	}

	// 3. Claim for provisioning using optimistic lock
	var claimedRec *entity.TenantReconcileRecord
	if optCfg.enforceDeadline {
		claimedRec, err = r.reconcileDAO.ClaimDueForProvisioning(ctx, silentDB, tenantID, rec.Version, time.Now())
	} else {
		claimedRec, err = r.reconcileDAO.ClaimForProvisioning(ctx, silentDB, tenantID, rec.Version)
	}
	if err != nil {
		if errors.Is(err, entity.ErrOptimisticLockConflict) || errors.Is(err, entity.ErrInvalidStateTransition) {
			// Another concurrent worker claimed or transitioned this record
			return nil
		}
		return err
	}
	rec = claimedRec

	// 4. Request virtual key from LiteLLM Gateway
	keyAlias := fmt.Sprintf("tenant_%s", tenantID)
	genReq := &gateway.GenerateKeyRequest{
		KeyName: keyAlias,
		TeamID:  tenantID,
		UserID:  tenantID,
		Models:  r.allowedModels,
	}

	genResp, err := r.gatewayClient.GenerateKey(ctx, genReq)
	if err != nil {
		// Transient failure: mark degraded using bounded recovery context in case ctx timed out
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		nextRetry := time.Now().Add(r.calculateBackoff(rec.RetryCount))
		var markErr error
		for attempt := 0; attempt < 3; attempt++ {
			_, markErr = r.reconcileDAO.MarkDegraded(recoveryCtx, silentDB, tenantID, rec.Version, err.Error(), nextRetry)
			if markErr == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if markErr != nil {
			return fmt.Errorf("failed to generate virtual key from gateway: %w (recovery mark degraded error: %v)", err, markErr)
		}
		return fmt.Errorf("failed to generate virtual key from gateway: %w", err)
	}

	virtualKey := genResp.Key
	if virtualKey == "" {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := errors.New("gateway returned empty virtual key")
		nextRetry := time.Now().Add(r.calculateBackoff(rec.RetryCount))
		_, _ = r.reconcileDAO.MarkDegraded(recoveryCtx, silentDB, tenantID, rec.Version, err.Error(), nextRetry)
		return err
	}

	// 5. Persist managed provider, instance, and models in local database transaction
	persistErr := silentDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A. TenantModelProvider
		var provider rentity.TenantModelProvider
		err := tx.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).
			First(&provider).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			provider = rentity.TenantModelProvider{
				ID:           generateID(),
				TenantID:     tenantID,
				ProviderName: ProviderOpenAIAPICompatible,
			}
			if err := tx.Create(&provider).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}

		// B. TenantModelInstance
		extraMap := map[string]interface{}{
			"base_url":   r.baseURL,
			"is_managed": true,
		}
		extraBytes, _ := json.Marshal(extraMap)
		extraJSON := string(extraBytes)

		var instance rentity.TenantModelInstance
		err = tx.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).
			First(&instance).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			instance = rentity.TenantModelInstance{
				ID:           generateID(),
				InstanceName: PlatformInstanceName,
				ProviderID:   provider.ID,
				APIKey:       virtualKey,
				Status:       "active",
				Extra:        extraJSON,
			}
			if err := tx.Create(&instance).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			// Update APIKey and extra on existing instance
			updates := map[string]interface{}{
				"api_key": virtualKey,
				"extra":   extraJSON,
				"status":  "active",
			}
			if err := tx.Model(&instance).Updates(updates).Error; err != nil {
				return err
			}
		}

		// C. TenantModel records (Chat + Embedding)
		modelExtraBytes, _ := json.Marshal(map[string]interface{}{"is_managed": true})
		modelExtraJSON := string(modelExtraBytes)

		modelsToEnsure := []struct {
			name      string
			modelType int
		}{
			{name: DefaultChatModelName, modelType: int(rentity.ModelTypeChat)},
			{name: DefaultEmbeddingModelName, modelType: int(rentity.ModelTypeEmbedding)},
		}

		var chatTenantModelID string
		var embdTenantModelID string

		for _, m := range modelsToEnsure {
			if !isModelPermitted(m.name, r.allowedModels) {
				continue
			}

			var tm rentity.TenantModel
			err := tx.Where("instance_id = ? AND model_name = ?", instance.ID, m.name).
				First(&tm).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				tm = rentity.TenantModel{
					ID:         generateID(),
					ModelName:  m.name,
					ProviderID: provider.ID,
					InstanceID: instance.ID,
					ModelType:  m.modelType,
					Status:     "active",
					Extra:      modelExtraJSON,
				}
				if err := tx.Create(&tm).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}

			if m.name == DefaultChatModelName {
				chatTenantModelID = tm.ID
			} else if m.name == DefaultEmbeddingModelName {
				embdTenantModelID = tm.ID
			}
		}

		// D. Atomically and conditionally initialize user_tenant default models
		var defaultsCfg DefaultModelConfig
		if chatTenantModelID != "" {
			defaultsCfg.ChatModelID = fmt.Sprintf("%s@%s@%s", DefaultChatModelName, PlatformInstanceName, ProviderOpenAIAPICompatible)
			defaultsCfg.ChatTenantModelID = chatTenantModelID
		}
		if embdTenantModelID != "" {
			defaultsCfg.EmbeddingModelID = fmt.Sprintf("%s@%s@%s", DefaultEmbeddingModelName, PlatformInstanceName, ProviderOpenAIAPICompatible)
			defaultsCfg.EmbeddingTenantModelID = embdTenantModelID
		}

		_, err = r.defaultsManager.InitializeTenantDefaults(ctx, tx, tenantID, defaultsCfg)
		if err != nil {
			return fmt.Errorf("failed to initialize tenant defaults: %w", err)
		}

		// E. Mark Ready in Reconcile state table
		keyHash := hashKey(virtualKey)
		aliasToSave := genResp.KeyName
		if aliasToSave == "" {
			aliasToSave = keyAlias
		}
		_, err = r.reconcileDAO.MarkReady(ctx, tx, tenantID, rec.Version, aliasToSave, keyHash)
		return err
	})

	// 6. Compensating Rollback if DB persistence failed
	if persistErr != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Verify whether transaction actually committed despite returning an error (e.g. connection drop after commit)
		if checkRec, err := r.reconcileDAO.GetByTenantID(rollbackCtx, silentDB, tenantID); err == nil && checkRec.Status == entity.StatusReady && checkRec.KeyHash == hashKey(virtualKey) {
			return nil
		}

		// Immediately revoke the issued virtual key to prevent orphaned credentials
		_, delErr := r.gatewayClient.DeleteKey(rollbackCtx, []string{virtualKey})

		if errors.Is(persistErr, entity.ErrOptimisticLockConflict) {
			if delErr != nil {
				return fmt.Errorf("compensating rollback incomplete: %w", delErr)
			}
			// A concurrent worker successfully reconciled this tenant; return nil cleanly
			return nil
		}

		// Mark degraded and schedule retry using bounded rollbackCtx
		errMsg := persistErr.Error()
		if delErr != nil {
			errMsg = fmt.Sprintf("%s; compensating rollback warning: %v", errMsg, delErr)
		}
		nextRetry := time.Now().Add(r.calculateBackoff(rec.RetryCount))
		_, _ = r.reconcileDAO.MarkDegraded(rollbackCtx, silentDB, tenantID, rec.Version, errMsg, nextRetry)

		if delErr != nil {
			return fmt.Errorf("failed to persist managed model records, compensating rollback incomplete: %w (delete error: %v)", persistErr, delErr)
		}
		return fmt.Errorf("failed to persist managed model records, compensating rollback executed: %w", persistErr)
	}

	return nil
}

func isModelPermitted(model string, allowed []string) bool {
	for _, a := range allowed {
		if a == model {
			return true
		}
	}
	return false
}

func (r *TenantReconciler) calculateBackoff(retries int) time.Duration {
	intervals := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
	}
	if retries < len(intervals) {
		return intervals[retries]
	}
	return 30 * time.Second
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)
}

func generateID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}
