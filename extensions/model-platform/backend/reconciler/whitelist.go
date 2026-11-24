package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

// instanceWhitelistLocks serializes whitelist rebuilds per platform instance in this process,
// across the embedding lifecycle and the tenant fan-out worker.
var instanceWhitelistLocks sync.Map

func instanceWhitelistMutex(instanceID string) *sync.Mutex {
	val, _ := instanceWhitelistLocks.LoadOrStore(instanceID, &sync.Mutex{})
	return val.(*sync.Mutex)
}

// LockInstanceWhitelist acquires the in-process lock guarding the virtual key whitelist of an
// instance and returns the function releasing it.
func LockInstanceWhitelist(instanceID string) (unlock func()) {
	mu := instanceWhitelistMutex(instanceID)
	mu.Lock()
	return mu.Unlock
}

type gatewayOverride int

const (
	gatewayOverrideNone gatewayOverride = iota
	gatewayOverrideInclude
	gatewayOverrideExclude
)

// rebuildGatewayWhitelist rewrites the tenant virtual key whitelist from the active tenant_model rows
// of the instance. LiteLLM /key/update replaces the whole models list, so the full set is always sent.
// overrideType is the model type of overrideModel and decides whether the embedding alias is required.
// An empty allowedModels permits every model. The caller must hold the instance whitelist lock.
func rebuildGatewayWhitelist(
	ctx context.Context,
	db *gorm.DB,
	client GatewayKeyClient,
	allowedModels []string,
	instance *rentity.TenantModelInstance,
	overrideModel string,
	overrideType int,
	overrideMode gatewayOverride,
) error {
	isPermitted := func(model string) bool {
		return len(allowedModels) == 0 || isModelPermitted(model, allowedModels)
	}

	var activeModels []rentity.TenantModel
	if err := db.WithContext(ctx).Where("instance_id = ? AND status = ?", instance.ID, "active").
		Find(&activeModels).Error; err != nil {
		return fmt.Errorf("failed to query active models for gateway key update: %w", err)
	}

	modelSet := make(map[string]struct{})
	hasActiveEmbedding := false
	for _, am := range activeModels {
		if overrideMode == gatewayOverrideExclude && am.ModelName == overrideModel {
			continue
		}
		modelSet[am.ModelName] = struct{}{}
		if am.ModelType == int(rentity.ModelTypeEmbedding) {
			hasActiveEmbedding = true
		}
	}
	if overrideMode == gatewayOverrideInclude && overrideModel != "" {
		modelSet[overrideModel] = struct{}{}
		if overrideType == int(rentity.ModelTypeEmbedding) {
			hasActiveEmbedding = true
		}
	}

	var whitelist []string
	if isPermitted("platform/chat") {
		whitelist = append(whitelist, "platform/chat")
	}
	if hasActiveEmbedding && isPermitted("platform/embedding") {
		whitelist = append(whitelist, "platform/embedding")
	}
	for mName := range modelSet {
		if isPermitted(mName) {
			whitelist = append(whitelist, mName)
		}
	}

	if len(whitelist) == 0 {
		whitelist = []string{RevokedModelPlaceholder}
	}

	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := client.UpdateKey(updateCtx, &gateway.UpdateKeyRequest{
		Key:    instance.APIKey,
		Models: whitelist,
	}); err != nil {
		return fmt.Errorf("failed to update gateway virtual key whitelist: %w", err)
	}
	return nil
}

// IncludeModelInWhitelist adds modelName to the instance virtual key whitelist while keeping every
// other active model. It runs before the tenant_model row is written, so a crash in between leaves
// a model authorized at the gateway but not yet visible, never the reverse.
// The caller must hold LockInstanceWhitelist for the instance.
func IncludeModelInWhitelist(
	ctx context.Context,
	db *gorm.DB,
	client GatewayKeyClient,
	instance *rentity.TenantModelInstance,
	modelName string,
	modelType int,
) error {
	return rebuildGatewayWhitelist(ctx, db, client, nil, instance, modelName, modelType, gatewayOverrideInclude)
}
