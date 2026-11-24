package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// NonEmbeddingLifecycleManager retires and reactivates stateless managed models (chat, rerank, vision and
// the like) for one tenant. They own no vector space, so there is no reference wait: a retired model goes
// straight from active to tombstoned and back. Every transition updates the gateway whitelist before the
// database row, and compensates by rebuilding the whitelist from the database when the row update loses.
type NonEmbeddingLifecycleManager struct {
	gatewayClient GatewayKeyClient
	modelLocks    sync.Map
}

// NewNonEmbeddingLifecycleManager creates a NonEmbeddingLifecycleManager.
func NewNonEmbeddingLifecycleManager(client GatewayKeyClient) *NonEmbeddingLifecycleManager {
	return &NonEmbeddingLifecycleManager{gatewayClient: client}
}

func (m *NonEmbeddingLifecycleManager) modelLock(tenantID, modelName string) *sync.Mutex {
	val, _ := m.modelLocks.LoadOrStore(tenantID+"/"+modelName, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// locate returns the tenant's managed instance and the model row. A missing model returns a nil row.
func (m *NonEmbeddingLifecycleManager) locate(db *gorm.DB, tenantID, modelName string, modelType int) (*rentity.TenantModelInstance, *rentity.TenantModel, error) {
	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).First(&provider).Error; err != nil {
		return nil, nil, fmt.Errorf("managed provider not found: %w", err)
	}
	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error; err != nil {
		return nil, nil, fmt.Errorf("managed instance not found: %w", err)
	}
	if instance.Status == "deleted" {
		return nil, nil, errors.New("managed instance is deleted")
	}

	var rows []rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instance.ID, modelName, modelType).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return &instance, nil, nil
	}
	return &instance, &rows[0], nil
}

// RetireModel tombstones the model for the tenant: it is removed from the virtual key whitelist, then the row
// is disabled. It returns false when there was nothing to do (missing, or already retired).
func (m *NonEmbeddingLifecycleManager) RetireModel(ctx context.Context, db *gorm.DB, tenantID, modelName string, modelType int) (bool, error) {
	if db == nil {
		return false, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return false, dao.ErrEmptyTenantID
	}
	if modelName == "" {
		return false, errors.New("modelName cannot be empty")
	}

	lock := m.modelLock(tenantID, modelName)
	lock.Lock()
	defer lock.Unlock()

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	instance, tm, err := m.locate(silentDB, tenantID, modelName, modelType)
	if err != nil {
		return false, err
	}
	if tm == nil {
		return false, nil
	}
	if _, tombstoned := RetirementFlags(tm.Extra); tombstoned || tm.Status != "active" {
		return false, nil
	}

	extra := parseExtraMap(tm.Extra)
	extra["is_managed"] = true
	extra["tombstoned"] = true
	extraBytes, _ := json.Marshal(extra)

	return m.transition(ctx, silentDB, instance, tm, modelName, modelType, gatewayOverrideExclude, map[string]interface{}{
		"status": "disabled",
		"extra":  string(extraBytes),
	}, "active")
}

// ReactivateModel makes a tombstoned model available again: it is added back to the virtual key whitelist,
// then the row is enabled and its retirement flags are cleared. It returns false when there was nothing to do.
func (m *NonEmbeddingLifecycleManager) ReactivateModel(ctx context.Context, db *gorm.DB, tenantID, modelName string, modelType int) (bool, error) {
	if db == nil {
		return false, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return false, dao.ErrEmptyTenantID
	}
	if modelName == "" {
		return false, errors.New("modelName cannot be empty")
	}

	lock := m.modelLock(tenantID, modelName)
	lock.Lock()
	defer lock.Unlock()

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	instance, tm, err := m.locate(silentDB, tenantID, modelName, modelType)
	if err != nil {
		return false, err
	}
	if tm == nil {
		return false, nil
	}
	deprecated, tombstoned := RetirementFlags(tm.Extra)
	if tm.Status == "active" && !deprecated && !tombstoned {
		return false, nil
	}
	if tm.Status == "deleted" {
		return false, errors.New("cannot reactivate a deleted model")
	}

	extra := parseExtraMap(tm.Extra)
	extra["is_managed"] = true
	delete(extra, "deprecated")
	delete(extra, "read_only")
	delete(extra, "tombstoned")
	extraBytes, _ := json.Marshal(extra)

	return m.transition(ctx, silentDB, instance, tm, modelName, modelType, gatewayOverrideInclude, map[string]interface{}{
		"status": "active",
		"extra":  string(extraBytes),
	}, tm.Status)
}

// transition applies a whitelist change followed by a compare-and-set of the model row. If the row changed
// concurrently or the update fails, the whitelist is rebuilt from the database so the key never keeps a
// state the rows do not back.
func (m *NonEmbeddingLifecycleManager) transition(
	ctx context.Context,
	db *gorm.DB,
	instance *rentity.TenantModelInstance,
	tm *rentity.TenantModel,
	modelName string,
	modelType int,
	mode gatewayOverride,
	updates map[string]interface{},
	expectedStatus string,
) (bool, error) {
	if instance.APIKey != "" && m.gatewayClient == nil {
		return false, errors.New("gateway client is required to change a model that has an active API key")
	}

	gwLock := instanceWhitelistMutex(instance.ID)
	gwLock.Lock()
	defer gwLock.Unlock()

	if instance.APIKey != "" {
		if err := rebuildGatewayWhitelist(ctx, db, m.gatewayClient, nil, instance, modelName, modelType, mode); err != nil {
			return false, err
		}
	}

	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)
	updates["update_time"] = &nowMilli
	updates["update_date"] = &nowDate

	res := db.Model(&rentity.TenantModel{}).
		Where("id = ? AND status = ? AND extra = ?", tm.ID, expectedStatus, tm.Extra).
		Updates(updates)
	if res.Error != nil || res.RowsAffected == 0 {
		m.compensate(ctx, db, instance)
		if res.Error != nil {
			return false, fmt.Errorf("failed to update model %s: %w", modelName, res.Error)
		}
		return false, nil
	}
	return true, nil
}

// compensate rebuilds the whitelist from the committed database state with an independent context, so a
// cancelled request cannot leave the gateway ahead of the rows.
func (m *NonEmbeddingLifecycleManager) compensate(ctx context.Context, db *gorm.DB, instance *rentity.TenantModelInstance) {
	if instance.APIKey == "" {
		return
	}
	compensateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = rebuildGatewayWhitelist(compensateCtx, db.WithContext(compensateCtx), m.gatewayClient, nil, instance, "", 0, gatewayOverrideNone)
}
