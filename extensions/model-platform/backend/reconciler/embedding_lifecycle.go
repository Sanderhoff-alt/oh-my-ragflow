package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// EmbeddingLifecycleState represents the three-stage lifecycle of an embedding model.
type EmbeddingLifecycleState string

const (
	// EmbeddingStateActive indicates the model is fully active for both existing and new knowledgebases.
	EmbeddingStateActive EmbeddingLifecycleState = "active"
	// EmbeddingStateDeprecated indicates the model is read-only: existing knowledgebases can still query it,
	// but it is hidden and blocked for new knowledgebase creation. It remains authorized on the gateway.
	EmbeddingStateDeprecated EmbeddingLifecycleState = "deprecated"
	// EmbeddingStateTombstoned indicates the model has 0 active knowledgebases, is disabled in the database,
	// and has been completely removed from the gateway virtual key whitelist.
	EmbeddingStateTombstoned EmbeddingLifecycleState = "tombstoned"
)

// EmbeddingRetireResult describes the outcome of an embedding model retirement attempt.
type EmbeddingRetireResult struct {
	ModelName        string
	State            EmbeddingLifecycleState
	ActiveKBCount    int64
	AllowedInGateway bool
	Message          string
}

// EmbeddingLifecycleManager coordinates the three-stage lifecycle for embedding models,
// ensuring zero disruption to existing knowledgebase vector search spaces.
type EmbeddingLifecycleManager struct {
	gatewayClient GatewayKeyClient
}

// NewEmbeddingLifecycleManager creates a new EmbeddingLifecycleManager.
func NewEmbeddingLifecycleManager(client GatewayKeyClient) *EmbeddingLifecycleManager {
	return &EmbeddingLifecycleManager{
		gatewayClient: client,
	}
}

// CheckEmbeddingUsage counts the number of active (non-deleted) knowledgebases using the specified embedding model.
func (m *EmbeddingLifecycleManager) CheckEmbeddingUsage(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	modelName string,
) (int64, error) {
	if db == nil {
		return 0, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return 0, dao.ErrEmptyTenantID
	}
	if modelName == "" {
		return 0, errors.New("modelName cannot be empty")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	// 1. Locate tenant model ID with explicit column selection to avoid join ID collisions
	var tm rentity.TenantModel
	err := silentDB.Table("tenant_model").
		Select("tenant_model.*").
		Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model.provider_id").
		Joins("JOIN tenant_model_instance ON tenant_model_instance.id = tenant_model.instance_id").
		Where("tenant_model_provider.tenant_id = ? AND tenant_model_provider.provider_name = ? AND tenant_model_instance.instance_name = ? AND tenant_model.model_name = ? AND tenant_model.model_type = ?",
			tenantID, ProviderOpenAIAPICompatible, PlatformInstanceName, modelName, int(rentity.ModelTypeEmbedding)).
		First(&tm).Error

	var tenantModelID string
	if err == nil {
		tenantModelID = tm.ID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, fmt.Errorf("failed to lookup tenant model: %w", err)
	}

	compositeID := fmt.Sprintf("%s@%s@%s", modelName, PlatformInstanceName, ProviderOpenAIAPICompatible)

	// 2. Count active knowledgebases (status IS NULL or status != '0')
	var count int64
	query := silentDB.Model(&rentity.Knowledgebase{}).
		Where("tenant_id = ? AND (status IS NULL OR status != '0')", tenantID)

	if tenantModelID != "" {
		query = query.Where("(embd_id = ? OR embd_id = ? OR embd_id = ? OR tenant_embd_id = ?)",
			modelName, compositeID, tenantModelID, tenantModelID)
	} else {
		query = query.Where("(embd_id = ? OR embd_id = ?)",
			modelName, compositeID)
	}

	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("failed to count active knowledgebases for embedding model: %w", err)
	}

	return count, nil
}

// RetireEmbeddingModel evaluates existing knowledgebases and transitions the model
// into either 'deprecated' (read-only, gateway access preserved) or 'tombstoned' (disabled & removed from gateway).
func (m *EmbeddingLifecycleManager) RetireEmbeddingModel(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	modelName string,
) (*EmbeddingRetireResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if modelName == "" {
		return nil, errors.New("modelName cannot be empty")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	// 1. Locate provider, instance, and tenant model
	var provider rentity.TenantModelProvider
	var instance rentity.TenantModelInstance
	var tm rentity.TenantModel

	err := silentDB.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).
		First(&provider).Error
	if err != nil {
		return nil, fmt.Errorf("managed provider not found: %w", err)
	}

	err = silentDB.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).
		First(&instance).Error
	if err != nil {
		return nil, fmt.Errorf("managed instance not found: %w", err)
	}

	err = silentDB.Where("instance_id = ? AND model_name = ? AND model_type = ?",
		instance.ID, modelName, int(rentity.ModelTypeEmbedding)).
		First(&tm).Error
	if err != nil {
		return nil, fmt.Errorf("embedding model %s not found: %w", modelName, err)
	}

	// 2. Count active knowledgebase usage
	activeCount, err := m.CheckEmbeddingUsage(ctx, db, tenantID, modelName)
	if err != nil {
		return nil, err
	}

	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	// Case A: Model has active knowledgebases -> Deprecated (Read-Only)
	if activeCount > 0 {
		extraMap := parseExtraMap(tm.Extra)
		extraMap["is_managed"] = true
		extraMap["deprecated"] = true
		extraMap["read_only"] = true
		extraBytes, _ := json.Marshal(extraMap)

		updates := map[string]interface{}{
			"status":      "active", // MUST remain active so existing KBs can query
			"extra":       string(extraBytes),
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}

		if err := silentDB.Model(&tm).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("failed to deprecate embedding model: %w", err)
		}

		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingStateDeprecated,
			ActiveKBCount:    activeCount,
			AllowedInGateway: true,
			Message:          "Model has active knowledgebases; transitioned to deprecated (read-only) with gateway access preserved.",
		}, nil
	}

	// Case B: Model has 0 active knowledgebases -> Tombstoned (Disabled + Removed from Gateway)
	return m.tombstoneModel(ctx, silentDB, tenantID, &instance, &tm, modelName)
}

func (m *EmbeddingLifecycleManager) tombstoneModel(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	instance *rentity.TenantModelInstance,
	tm *rentity.TenantModel,
	modelName string,
) (*EmbeddingRetireResult, error) {
	// 1. Synchronize gateway key whitelist first if gateway client and APIKey are present
	if m.gatewayClient != nil && instance.APIKey != "" {
		var activeModels []rentity.TenantModel
		if err := db.Where("instance_id = ? AND status = ? AND id != ?", instance.ID, "active", tm.ID).
			Find(&activeModels).Error; err != nil {
			return nil, fmt.Errorf("failed to query active models for gateway key update: %w", err)
		}

		remainingModelNames := []string{
			"platform/chat",
		}
		hasActiveEmbedding := false
		for _, am := range activeModels {
			if am.ModelType == int(rentity.ModelTypeEmbedding) {
				hasActiveEmbedding = true
			}
			if am.ModelName != modelName {
				remainingModelNames = append(remainingModelNames, am.ModelName)
			}
		}
		if hasActiveEmbedding {
			remainingModelNames = append(remainingModelNames, "platform/embedding")
		}

		updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		if _, err := m.gatewayClient.UpdateKey(updateCtx, &gateway.UpdateKeyRequest{
			Key:    instance.APIKey,
			Models: remainingModelNames,
		}); err != nil {
			return nil, fmt.Errorf("failed to update gateway virtual key whitelist: %w", err)
		}
	}

	// 2. Finalize database state as tombstoned (status: disabled)
	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	extraMap := parseExtraMap(tm.Extra)
	extraMap["is_managed"] = true
	extraMap["deprecated"] = true
	extraMap["tombstoned"] = true
	extraBytes, _ := json.Marshal(extraMap)

	updates := map[string]interface{}{
		"status":      "disabled",
		"extra":       string(extraBytes),
		"update_time": &nowMilli,
		"update_date": &nowDate,
	}

	if err := db.Model(tm).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("failed to tombstone model in database: %w", err)
	}

	return &EmbeddingRetireResult{
		ModelName:        modelName,
		State:            EmbeddingStateTombstoned,
		ActiveKBCount:    0,
		AllowedInGateway: false,
		Message:          "Model has 0 active knowledgebases; tombstoned and removed from gateway virtual key whitelist.",
	}, nil
}

// DeleteModelPermanently deletes an embedding model only when no active knowledgebases reference it,
// updating the LiteLLM gateway virtual key whitelist and removing the model record from the database.
func (m *EmbeddingLifecycleManager) DeleteModelPermanently(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	modelName string,
) error {
	if db == nil {
		return dao.ErrNilTransaction
	}
	if tenantID == "" {
		return dao.ErrEmptyTenantID
	}
	if modelName == "" {
		return errors.New("modelName cannot be empty")
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Check whether any active knowledgebase references this model
		activeCount, err := m.CheckEmbeddingUsage(ctx, tx, tenantID, modelName)
		if err != nil {
			return err
		}
		if activeCount > 0 {
			return fmt.Errorf("cannot delete model %q: %d active knowledgebases are still referencing it", modelName, activeCount)
		}

		// 2. Find provider and instance
		var provider rentity.TenantModelProvider
		var instance rentity.TenantModelInstance
		var tm rentity.TenantModel

		err = tx.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).First(&provider).Error
		if err != nil {
			return fmt.Errorf("managed provider not found: %w", err)
		}

		err = tx.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error
		if err != nil {
			return fmt.Errorf("managed instance not found: %w", err)
		}

		err = tx.Where("instance_id = ? AND model_name = ? AND model_type = ?",
			instance.ID, modelName, int(rentity.ModelTypeEmbedding)).First(&tm).Error
		if err != nil {
			return fmt.Errorf("embedding model %s not found: %w", modelName, err)
		}

		// 3. Synchronize gateway key whitelist first if gatewayClient and APIKey are present
		if m.gatewayClient != nil && instance.APIKey != "" {
			var activeModels []rentity.TenantModel
			if err := tx.Where("instance_id = ? AND status = ? AND id != ?", instance.ID, "active", tm.ID).
				Find(&activeModels).Error; err != nil {
				return fmt.Errorf("failed to query active models for gateway key update: %w", err)
			}

			remainingModelNames := []string{
				"platform/chat",
			}
			hasActiveEmbedding := false
			for _, am := range activeModels {
				if am.ModelType == int(rentity.ModelTypeEmbedding) {
					hasActiveEmbedding = true
				}
				if am.ModelName != modelName {
					remainingModelNames = append(remainingModelNames, am.ModelName)
				}
			}
			if hasActiveEmbedding {
				remainingModelNames = append(remainingModelNames, "platform/embedding")
			}

			updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			if _, err := m.gatewayClient.UpdateKey(updateCtx, &gateway.UpdateKeyRequest{
				Key:    instance.APIKey,
				Models: remainingModelNames,
			}); err != nil {
				return fmt.Errorf("failed to update gateway virtual key whitelist: %w", err)
			}
		}

		// 4. Delete the model record
		if err := tx.Delete(&tm).Error; err != nil {
			return fmt.Errorf("failed to delete model record: %w", err)
		}

		return nil
	})
}

// SweepTombstoneCandidates scans all deprecated embedding models for the tenant.
// If any deprecated model no longer has active knowledgebases, it is finalized into tombstoned state.
func (m *EmbeddingLifecycleManager) SweepTombstoneCandidates(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
) (int, error) {
	if db == nil {
		return 0, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return 0, dao.ErrEmptyTenantID
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	var provider rentity.TenantModelProvider
	var instance rentity.TenantModelInstance

	err := silentDB.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).
		First(&provider).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to lookup managed provider: %w", err)
	}

	err = silentDB.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).
		First(&instance).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to lookup managed instance: %w", err)
	}

	var models []rentity.TenantModel
	err = silentDB.Where("instance_id = ? AND model_type = ? AND status = ?",
		instance.ID, int(rentity.ModelTypeEmbedding), "active").
		Find(&models).Error
	if err != nil {
		return 0, fmt.Errorf("failed to query active embedding models: %w", err)
	}

	tombstoned := 0
	var errs []error
	for _, model := range models {
		select {
		case <-ctx.Done():
			return tombstoned, ctx.Err()
		default:
		}

		if !isDeprecated(model.Extra) {
			continue
		}

		count, err := m.CheckEmbeddingUsage(ctx, db, tenantID, model.ModelName)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to check embedding usage for %s: %w", model.ModelName, err))
			continue
		}
		if count == 0 {
			res, err := m.tombstoneModel(ctx, silentDB, tenantID, &instance, &model, model.ModelName)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to tombstone model %s: %w", model.ModelName, err))
				continue
			}
			if res.State == EmbeddingStateTombstoned {
				tombstoned++
			}
		}
	}

	return tombstoned, errors.Join(errs...)
}

// FilterAvailableForCreation filters a list of TenantModel records, excluding any model
// that is disabled, deprecated, read-only, or tombstoned, ensuring only healthy active models
// are presented for new knowledgebase creation.
func FilterAvailableForCreation(models []rentity.TenantModel) []rentity.TenantModel {
	var available []rentity.TenantModel
	for _, m := range models {
		if m.Status != "active" {
			continue
		}
		if isDeprecatedOrReadOnly(m.Extra) {
			continue
		}
		available = append(available, m)
	}
	return available
}

func parseExtraMap(extraJSON string) map[string]interface{} {
	m := make(map[string]interface{})
	if extraJSON == "" {
		return m
	}
	_ = json.Unmarshal([]byte(extraJSON), &m)
	if m == nil {
		m = make(map[string]interface{})
	}
	return m
}

func isDeprecated(extraJSON string) bool {
	if extraJSON == "" {
		return false
	}
	m := parseExtraMap(extraJSON)
	v, ok := m["deprecated"].(bool)
	return ok && v
}

func isDeprecatedOrReadOnly(extraJSON string) bool {
	if extraJSON == "" {
		return false
	}
	m := parseExtraMap(extraJSON)
	if dep, ok := m["deprecated"].(bool); ok && dep {
		return true
	}
	if ro, ok := m["read_only"].(bool); ok && ro {
		return true
	}
	if tomb, ok := m["tombstoned"].(bool); ok && tomb {
		return true
	}
	return strings.Contains(extraJSON, `"deprecated":true`) ||
		strings.Contains(extraJSON, `"read_only":true`) ||
		strings.Contains(extraJSON, `"tombstoned":true`)
}
