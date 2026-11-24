package reconciler

import (
	"context"
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
	// DefaultGatewayBaseURL is the internal loopback proxy endpoint.
	DefaultGatewayBaseURL = "http://127.0.0.1:4000"
)

// managedModelRef is the composite reference stored in user_tenant for a managed model.
func managedModelRef(modelName string) string {
	return fmt.Sprintf("%s@%s@%s", modelName, PlatformInstanceName, ProviderOpenAIAPICompatible)
}

// activeCatalogModels returns the active catalog models a new tenant is provisioned with. A non-empty
// allowedModels restricts them, an empty one permits every model.
func (r *TenantReconciler) activeCatalogModels(ctx context.Context, db *gorm.DB) ([]entity.PlatformManagedModel, error) {
	var rows []entity.PlatformManagedModel
	if err := db.WithContext(ctx).Where("status = ?", entity.ManagedModelStatusActive).
		Order("model_name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(r.allowedModels) == 0 {
		return rows, nil
	}
	permitted := rows[:0]
	for _, m := range rows {
		if isModelPermitted(m.ModelName, r.allowedModels) {
			permitted = append(permitted, m)
		}
	}
	return permitted, nil
}

// initialWhitelist is the model list of a new tenant's virtual key. LiteLLM treats an empty list as
// unrestricted and the gateway client rejects it, so an empty catalog yields the deny-all placeholder.
// The two platform aliases follow allowedModels like every other name, the same as in the whitelist rebuild.
func initialWhitelist(catalog []entity.PlatformManagedModel, allowedModels []string) []string {
	permitted := func(model string) bool {
		return len(allowedModels) == 0 || isModelPermitted(model, allowedModels)
	}
	var whitelist []string
	if len(catalog) > 0 && permitted("platform/chat") {
		whitelist = append(whitelist, "platform/chat")
	}
	for _, m := range catalog {
		if m.ModelType == int(rentity.ModelTypeEmbedding) {
			if permitted("platform/embedding") {
				whitelist = append(whitelist, "platform/embedding")
			}
			break
		}
	}
	for _, m := range catalog {
		whitelist = append(whitelist, m.ModelName)
	}
	if len(whitelist) == 0 {
		return []string{RevokedModelPlaceholder}
	}
	return whitelist
}

// GatewayKeyClient defines the contract for LiteLLM virtual key management.
type GatewayKeyClient interface {
	GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error)
	UpdateKey(ctx context.Context, req *gateway.UpdateKeyRequest) (*gateway.UpdateKeyResponse, error)
	DeleteKeysByHash(ctx context.Context, hashes []string) error
	ListTeamKeys(ctx context.Context, teamID string) ([]gateway.TeamKey, error)
}

// TenantReconciler handles idempotent provisioning of tenant models and virtual keys.
type TenantReconciler struct {
	gatewayClient   GatewayKeyClient
	reconcileDAO    *dao.ReconcileDAO
	defaultsManager *DefaultsManager
	baseURL         string
	allowedModels   []string
}

// NewTenantReconciler creates a new TenantReconciler. The models a tenant is provisioned with come from
// the managed model catalog. A non-empty allowedModels restricts them, an empty one permits every model.
func NewTenantReconciler(client GatewayKeyClient, baseURL string, allowedModels []string) *TenantReconciler {
	if baseURL == "" {
		baseURL = DefaultGatewayBaseURL
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

	// 3. Read the catalog before claiming, so a catalog read failure leaves the record untouched for the next attempt
	catalog, err := r.activeCatalogModels(ctx, silentDB)
	if err != nil {
		return fmt.Errorf("failed to read the managed model catalog: %w", err)
	}

	// 3.1 Claim for provisioning using optimistic lock
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

	// 4. Drop keys that earlier failed attempts left behind, then request the virtual key from LiteLLM Gateway.
	// A tenant that is not ready has no committed credential, so the gateway team must end up with this one key.
	genReq := newTenantKeyRequest(tenantID, initialWhitelist(catalog, r.allowedModels))

	genResp, err := r.issueFreshKey(ctx, tenantID, genReq)
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
		ownerUserID, err := resolveTenantOwnerUserID(tx, tenantID)
		if err != nil {
			return err
		}

		var provider rentity.TenantModelProvider
		err = tx.Where("tenant_id = ? AND user_id = ? AND provider_name = ?", tenantID, ownerUserID, ProviderOpenAIAPICompatible).
			First(&provider).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			provider = rentity.TenantModelProvider{
				ID:           generateID(),
				UserID:       ownerUserID,
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
		// Upstream parses TenantModelInstance.Extra as map[string]string across provider
		// listing, model factory, agent, and ingestion paths, so every value must be a string.
		// OpenAI-API-Compatible URL suffixes omit "/v1", so normalize the gateway URL with "/v1".
		extraMap := map[string]string{
			"base_url":   normalizeOpenAIBaseURL(r.baseURL),
			"is_managed": "true",
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

		// C. TenantModel records for every active catalog model
		modelExtraBytes, _ := json.Marshal(map[string]interface{}{"is_managed": true})
		modelExtraJSON := string(modelExtraBytes)

		defaultsCfg := make(DefaultModelConfig)
		for _, m := range catalog {
			var tm rentity.TenantModel
			err := tx.Where("instance_id = ? AND model_name = ?", instance.ID, m.ModelName).
				First(&tm).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				tm = rentity.TenantModel{
					ID:         generateID(),
					ModelName:  m.ModelName,
					ProviderID: provider.ID,
					InstanceID: instance.ID,
					ModelType:  m.ModelType,
					Status:     "active",
					Extra:      modelExtraJSON,
				}
				if err := tx.Create(&tm).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}

			if m.IsDefault && entity.IsManagedModelType(m.ModelType) {
				defaultsCfg[rentity.ModelType(m.ModelType)] = DefaultSlot{
					ModelID:       managedModelRef(m.ModelName),
					TenantModelID: tm.ID,
				}
			}
		}

		// D. Atomically and conditionally initialize user_tenant default models from the catalog defaults
		_, err = r.defaultsManager.InitializeTenantDefaults(ctx, tx, tenantID, defaultsCfg)
		if err != nil {
			return fmt.Errorf("failed to initialize tenant defaults: %w", err)
		}

		// E. Mark Ready in Reconcile state table
		keyHash := gateway.HashKey(virtualKey)
		aliasToSave := genResp.KeyName
		if aliasToSave == "" {
			aliasToSave = genReq.KeyName
		}
		_, err = r.reconcileDAO.MarkReady(ctx, tx, tenantID, rec.Version, aliasToSave, keyHash)
		return err
	})

	// 6. Compensating Rollback if DB persistence failed
	if persistErr != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Verify whether transaction actually committed despite returning an error (e.g. connection drop after commit)
		if checkRec, err := r.reconcileDAO.GetByTenantID(rollbackCtx, silentDB, tenantID); err == nil && checkRec.Status == entity.StatusReady && checkRec.KeyHash == gateway.HashKey(virtualKey) {
			return nil
		}

		// Immediately revoke the issued virtual key, addressed by its own hash, to prevent orphaned credentials
		delErr := r.gatewayClient.DeleteKeysByHash(rollbackCtx, []string{gateway.HashKey(virtualKey)})

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

// issueFreshKey leaves the tenant's gateway team with exactly one key: it revokes any key a failed earlier
// attempt left behind and then issues the requested one.
func (r *TenantReconciler) issueFreshKey(ctx context.Context, tenantID string, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	if _, err := RevokeTenantKeys(ctx, r.gatewayClient, tenantID); err != nil {
		return nil, err
	}
	return r.gatewayClient.GenerateKey(ctx, req)
}

// newKeyAlias names a freshly issued key. The alias is a human readable label only: the gateway does not
// reliably enforce its uniqueness and revocation addresses keys by hash, so the random suffix simply lets
// the old and the new key of a rotation coexist.
func newKeyAlias(tenantID string) string {
	return fmt.Sprintf("tenant_%s_%s", tenantID, generateID()[:8])
}

// newTenantKeyRequest builds the request for a tenant's virtual key. The gateway team is the tenant id, so
// every key of a tenant can be found, converged and revoked through its team.
func newTenantKeyRequest(tenantID string, models []string) *gateway.GenerateKeyRequest {
	return &gateway.GenerateKeyRequest{
		KeyName: newKeyAlias(tenantID),
		TeamID:  tenantID,
		UserID:  tenantID,
		Models:  models,
	}
}

// resolveTenantOwnerUserID returns the active owner's user_id for tenantID,
// falling back to tenantID when no owner membership exists yet (community tenants use tenantID == userID).
func resolveTenantOwnerUserID(tx *gorm.DB, tenantID string) (string, error) {
	var owners []rentity.UserTenant
	err := tx.Where("tenant_id = ? AND role = ? AND (status IS NULL OR status = ?)", tenantID, "owner", "1").
		Limit(1).Find(&owners).Error
	if err != nil {
		return "", fmt.Errorf("failed to resolve tenant owner for %s: %w", tenantID, err)
	}
	if len(owners) > 0 && strings.TrimSpace(owners[0].UserID) != "" {
		return strings.TrimSpace(owners[0].UserID), nil
	}
	return tenantID, nil
}

// normalizeOpenAIBaseURL ensures the OpenAI-API-Compatible instance base_url ends with "/v1"
// while keeping gateway management calls on the versionless root URL.
func normalizeOpenAIBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = DefaultGatewayBaseURL
	}
	if strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		return trimmed
	}
	return trimmed + "/v1"
}

func generateID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}
