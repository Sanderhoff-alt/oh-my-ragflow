package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
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

	// RevokedModelPlaceholder is sent in LiteLLM /key/update when all models are tombstoned
	// to prevent omitempty from omitting the models field and leaving the previous policy intact.
	RevokedModelPlaceholder = "__ragflow_revoked__"
)

// EmbeddingRetireResult describes the outcome of an embedding model retirement attempt.
type EmbeddingRetireResult struct {
	ModelName        string
	State            EmbeddingLifecycleState
	ActiveKBCount    int64
	AllowedInGateway bool
	Message          string
	Transitioned     bool
}

// EmbeddingLifecycleManager coordinates the three-stage lifecycle for embedding models,
// ensuring zero disruption to existing knowledgebase vector search spaces.
type EmbeddingLifecycleManager struct {
	gatewayClient        GatewayKeyClient
	allowedModels        []string
	modelLocks           sync.Map
	onBeforeTombstone    func(tenantID, modelName string)
	onBeforeTombstoneDB  func(tenantID, modelName string)
	onBeforeDeprecateDB  func(tenantID, modelName string)
	onBeforeReactivateDB func(tenantID, modelName string)
}

// NewEmbeddingLifecycleManager creates a new EmbeddingLifecycleManager.
func NewEmbeddingLifecycleManager(client GatewayKeyClient, allowedModels ...[]string) *EmbeddingLifecycleManager {
	var models []string
	if len(allowedModels) > 0 && len(allowedModels[0]) > 0 {
		models = allowedModels[0]
	}
	return &EmbeddingLifecycleManager{
		gatewayClient: client,
		allowedModels: models,
	}
}

func (m *EmbeddingLifecycleManager) isPermitted(model string) bool {
	if len(m.allowedModels) == 0 {
		return true
	}
	return isModelPermitted(model, m.allowedModels)
}

func (m *EmbeddingLifecycleManager) getModelLock(tenantID, modelName string) *sync.Mutex {
	key := tenantID + "/" + modelName
	val, _ := m.modelLocks.LoadOrStore(key, &sync.Mutex{})
	return val.(*sync.Mutex)
}

func (m *EmbeddingLifecycleManager) getGatewayLock(instanceID string) *sync.Mutex {
	return instanceWhitelistMutex(instanceID)
}

func (m *EmbeddingLifecycleManager) reconcileGatewayWhitelistLocked(
	ctx context.Context,
	db *gorm.DB,
	instance *rentity.TenantModelInstance,
	overrideModel string,
	overrideMode gatewayOverride,
) error {
	return rebuildGatewayWhitelist(ctx, db, m.gatewayClient, m.allowedModels, instance,
		overrideModel, int(rentity.ModelTypeEmbedding), overrideMode)
}

func (m *EmbeddingLifecycleManager) reconcileGatewayWhitelist(
	ctx context.Context,
	db *gorm.DB,
	instance *rentity.TenantModelInstance,
	overrideModel string,
	overrideMode gatewayOverride,
) error {
	gwLock := m.getGatewayLock(instance.ID)
	gwLock.Lock()
	defer gwLock.Unlock()

	return m.reconcileGatewayWhitelistLocked(ctx, db, instance, overrideModel, overrideMode)
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

	lock := m.getModelLock(tenantID, modelName)
	lock.Lock()
	defer lock.Unlock()

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
		wasAlreadyDeprecated := isDeprecated(tm.Extra)

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

		if m.onBeforeDeprecateDB != nil {
			m.onBeforeDeprecateDB(tenantID, modelName)
		}

		res := silentDB.Model(&rentity.TenantModel{}).
			Where("id = ? AND status = ? AND extra = ?", tm.ID, "active", tm.Extra).
			Updates(updates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to deprecate embedding model: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			// Row was modified concurrently (e.g. tombstoned or reactivated). Reload committed state.
			var winningTM rentity.TenantModel
			if err := silentDB.Where("id = ?", tm.ID).First(&winningTM).Error; err != nil {
				return nil, fmt.Errorf("failed to reload winning model state: %w", err)
			}
			winningExtra := parseExtraMap(winningTM.Extra)
			if winningTM.Status == "deleted" || winningTM.Status == "disabled" || winningExtra["tombstoned"] == true {
				return &EmbeddingRetireResult{
					ModelName:        modelName,
					State:            EmbeddingStateTombstoned,
					ActiveKBCount:    activeCount,
					AllowedInGateway: false,
					Message:          "Model was tombstoned concurrently.",
					Transitioned:     false,
				}, nil
			}
			state := EmbeddingStateActive
			if isDeprecated(winningTM.Extra) {
				state = EmbeddingStateDeprecated
			}
			return &EmbeddingRetireResult{
				ModelName:        modelName,
				State:            state,
				ActiveKBCount:    activeCount,
				AllowedInGateway: true,
				Message:          "Concurrent modification detected; deprecation aborted.",
				Transitioned:     false,
			}, nil
		}

		msg := "Model has active knowledgebases; transitioned to deprecated (read-only) with gateway access preserved."
		if wasAlreadyDeprecated {
			msg = "Model has active knowledgebases; already in deprecated (read-only) state."
		}

		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingStateDeprecated,
			ActiveKBCount:    activeCount,
			AllowedInGateway: true,
			Message:          msg,
			Transitioned:     !wasAlreadyDeprecated,
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
	// Re-verify latest model state from DB to prevent concurrent race with reactivation
	var currentTM rentity.TenantModel
	if err := db.Where("id = ?", tm.ID).First(&currentTM).Error; err != nil {
		return nil, fmt.Errorf("failed to reload model %s: %w", modelName, err)
	}

	currentExtra := parseExtraMap(currentTM.Extra)
	if currentExtra["tombstoned"] == true || currentTM.Status == "disabled" {
		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingStateTombstoned,
			ActiveKBCount:    0,
			AllowedInGateway: false,
			Message:          "Model is already tombstoned.",
		}, nil
	}

	if currentTM.Status != "active" {
		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingLifecycleState(currentTM.Status),
			ActiveKBCount:    0,
			AllowedInGateway: false,
			Message:          "Model is not active; skipping tombstone.",
		}, nil
	}

	// If tm was passed from sweep (where tm.Extra was deprecated), but currentTM is no longer deprecated,
	// a concurrent ReactivateEmbeddingModel completed and cleared the deprecated flag. Abort tombstone.
	if isDeprecated(tm.Extra) && !isDeprecated(currentTM.Extra) {
		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingStateActive,
			ActiveKBCount:    0,
			AllowedInGateway: true,
			Message:          "Model was reactivated concurrently; skipping tombstone.",
		}, nil
	}

	// 1. Synchronize gateway key whitelist first if instance has an API key
	gwLock := m.getGatewayLock(instance.ID)
	gwLock.Lock()
	defer gwLock.Unlock()

	if instance.APIKey != "" {
		if m.gatewayClient == nil {
			return nil, errors.New("gateway client is required to tombstone model when instance has active API key")
		}

		if err := m.reconcileGatewayWhitelistLocked(ctx, db, instance, modelName, gatewayOverrideExclude); err != nil {
			return nil, fmt.Errorf("failed to update gateway virtual key whitelist: %w", err)
		}
	}

	if m.onBeforeTombstoneDB != nil {
		m.onBeforeTombstoneDB(tenantID, modelName)
	}

	// 2. Finalize database state as tombstoned (status: disabled) with atomic condition
	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	extraMap := parseExtraMap(currentTM.Extra)
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

	res := db.Model(&rentity.TenantModel{}).
		Where("id = ? AND status = ? AND extra = ?", currentTM.ID, "active", currentTM.Extra).
		Updates(updates)
	if res.Error != nil {
		return nil, fmt.Errorf("failed to tombstone model in database: %w", res.Error)
	}

	if res.RowsAffected == 0 {
		// Concurrent state modification detected (e.g. ReactivateEmbeddingModel or another sweep worker ran).
		// Reload current committed state to determine the winning state accurately.
		var winningTM rentity.TenantModel
		if err := db.Where("id = ?", currentTM.ID).First(&winningTM).Error; err != nil {
			return nil, fmt.Errorf("failed to reload winning model state: %w", err)
		}

		winningExtra := parseExtraMap(winningTM.Extra)
		if winningTM.Status == "disabled" || winningExtra["tombstoned"] == true {
			// Another process finalized tombstoning; gateway was already updated to exclude model.
			return &EmbeddingRetireResult{
				ModelName:        modelName,
				State:            EmbeddingStateTombstoned,
				ActiveKBCount:    0,
				AllowedInGateway: false,
				Message:          "Model was tombstoned concurrently.",
			}, nil
		}

		// Otherwise, winning state is active/reactivated. Restore gateway key whitelist from committed DB state.
		if instance.APIKey != "" && m.gatewayClient != nil {
			if err := m.reconcileGatewayWhitelistLocked(ctx, db, instance, "", gatewayOverrideNone); err != nil {
				return nil, fmt.Errorf("failed to compensate gateway whitelist after concurrent modification: %w", err)
			}
		}
		return &EmbeddingRetireResult{
			ModelName:        modelName,
			State:            EmbeddingStateActive,
			ActiveKBCount:    0,
			AllowedInGateway: true,
			Message:          "Concurrent modification detected; tombstone aborted and gateway access preserved.",
		}, nil
	}

	return &EmbeddingRetireResult{
		ModelName:        modelName,
		State:            EmbeddingStateTombstoned,
		ActiveKBCount:    0,
		AllowedInGateway: false,
		Message:          "Model has 0 active knowledgebases; tombstoned and removed from gateway virtual key whitelist.",
		Transitioned:     true,
	}, nil
}

// ReactivateEmbeddingModel reactivates a deprecated or tombstoned embedding model back to active status,
// ensuring the LiteLLM gateway whitelist includes the model and restoring normal selection in the UI.
func (m *EmbeddingLifecycleManager) ReactivateEmbeddingModel(
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

	lock := m.getModelLock(tenantID, modelName)
	lock.Lock()
	defer lock.Unlock()

	silentDB := db.WithContext(ctx)

	var provider rentity.TenantModelProvider
	if err := silentDB.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).First(&provider).Error; err != nil {
		return fmt.Errorf("managed provider not found: %w", err)
	}

	var instance rentity.TenantModelInstance
	if err := silentDB.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error; err != nil {
		return fmt.Errorf("managed instance not found: %w", err)
	}

	var tm rentity.TenantModel
	if err := silentDB.Where("instance_id = ? AND model_name = ? AND model_type = ?",
		instance.ID, modelName, int(rentity.ModelTypeEmbedding)).First(&tm).Error; err != nil {
		return fmt.Errorf("embedding model %s not found: %w", modelName, err)
	}

	if instance.Status == "deleted" || tm.Status == "deleted" {
		return errors.New("cannot reactivate model on deleted instance or tenant")
	}

	if !m.isPermitted(modelName) {
		return fmt.Errorf("embedding model %s is not permitted by allowlist", modelName)
	}

	gwLock := m.getGatewayLock(instance.ID)
	gwLock.Lock()
	defer gwLock.Unlock()

	// 1. Ensure model is authorized on LiteLLM gateway key whitelist if instance has an API key
	if instance.APIKey != "" {
		if m.gatewayClient == nil {
			return errors.New("gateway client is required to reactivate model when instance has active API key")
		}

		if err := m.reconcileGatewayWhitelistLocked(ctx, silentDB, &instance, modelName, gatewayOverrideInclude); err != nil {
			return err
		}
	}

	// 2. Finalize database state: status = active, clear deprecated and tombstoned flags
	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	extraMap := parseExtraMap(tm.Extra)
	extraMap["is_managed"] = true
	delete(extraMap, "deprecated")
	delete(extraMap, "read_only")
	delete(extraMap, "tombstoned")
	extraBytes, _ := json.Marshal(extraMap)

	updates := map[string]interface{}{
		"status":      "active",
		"extra":       string(extraBytes),
		"update_time": &nowMilli,
		"update_date": &nowDate,
	}

	if m.onBeforeReactivateDB != nil {
		m.onBeforeReactivateDB(tenantID, modelName)
	}

	res := silentDB.Model(&rentity.TenantModel{}).
		Where("id = ? AND status = ? AND extra = ?", tm.ID, tm.Status, tm.Extra).
		Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		// Roll back gateway key whitelist to match committed database rows using bounded independent context
		if instance.APIKey != "" && m.gatewayClient != nil {
			compensateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if compErr := m.reconcileGatewayWhitelistLocked(compensateCtx, silentDB.WithContext(compensateCtx), &instance, "", gatewayOverrideNone); compErr != nil {
				return fmt.Errorf("failed to reactivate model in database (%w); gateway compensation failed: %v", res.Error, compErr)
			}
		}
		if res.Error != nil {
			return fmt.Errorf("failed to reactivate model in database: %w", res.Error)
		}
		return errors.New("concurrent state modification detected; reactivation aborted")
	}

	return nil
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

		func() {
			lock := m.getModelLock(tenantID, model.ModelName)
			lock.Lock()
			defer lock.Unlock()

			var latestModel rentity.TenantModel
			if err := silentDB.Where("id = ?", model.ID).First(&latestModel).Error; err != nil {
				errs = append(errs, fmt.Errorf("failed to reload model %s: %w", model.ModelName, err))
				return
			}

			if latestModel.Status != "active" || !isDeprecated(latestModel.Extra) {
				// Concurrently reactivated or state modified, skip
				return
			}

			if m.onBeforeTombstone != nil {
				m.onBeforeTombstone(tenantID, latestModel.ModelName)
			}

			count, err := m.CheckEmbeddingUsage(ctx, db, tenantID, latestModel.ModelName)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to check embedding usage for %s: %w", latestModel.ModelName, err))
				return
			}
			if count == 0 {
				res, err := m.tombstoneModel(ctx, silentDB, tenantID, &instance, &latestModel, latestModel.ModelName)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to tombstone model %s: %w", latestModel.ModelName, err))
					return
				}
				if res != nil && res.State == EmbeddingStateTombstoned && res.Transitioned {
					tombstoned++
				}
			}
		}()
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
	decoder := json.NewDecoder(strings.NewReader(extraJSON))
	decoder.UseNumber()
	_ = decoder.Decode(&m)
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
	return false
}
