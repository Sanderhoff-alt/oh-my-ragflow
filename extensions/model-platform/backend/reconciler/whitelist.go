package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// WithInstanceRowLock runs fn as the only writer of an instance's whitelist and tenant_model rows across
// replicas, using a row lock on the tenant_model_instance row held in a separate transaction while fn
// runs on its own connection. It does not take the in-process mutex, so it is meant for callers such as
// the lifecycle managers that take that mutex themselves. SQLite has no row locks and only runs tests.
func WithInstanceRowLock(ctx context.Context, db *gorm.DB, instanceID string, fn func() error) error {
	if db.Dialector.Name() == "sqlite" {
		return fn()
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var instance rentity.TenantModelInstance
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
			Where("id = ?", instanceID).First(&instance).Error; err != nil {
			return fmt.Errorf("failed to lock instance %s: %w", instanceID, err)
		}
		return fn()
	})
}

// WithInstanceWhitelistLock is WithInstanceRowLock plus the in-process mutex, which also serializes the
// goroutines of one replica and makes SQLite, which has no row locks, behave the same in tests. The order
// is always row lock first, mutex second: the lifecycle managers take the mutex inside a caller that
// already holds the row lock, so the reverse order here would deadlock against them.
func WithInstanceWhitelistLock(ctx context.Context, db *gorm.DB, instanceID string, fn func() error) error {
	return WithInstanceRowLock(ctx, db, instanceID, func() error {
		unlock := LockInstanceWhitelist(instanceID)
		defer unlock()
		return fn()
	})
}

// RetirementFlags reports the per-tenant retirement state recorded in a tenant_model extra value:
// deprecated (read-only but still authorized) and tombstoned (disabled and removed from the whitelist).
func RetirementFlags(extraJSON string) (deprecated, tombstoned bool) {
	extra := parseExtraMap(extraJSON)
	return extra["deprecated"] == true, extra["tombstoned"] == true
}

type gatewayOverride int

const (
	gatewayOverrideNone gatewayOverride = iota
	gatewayOverrideInclude
	gatewayOverrideExclude
)

// gatewayWhitelist computes the tenant virtual key whitelist from the active tenant_model rows of the
// instance. overrideType is the model type of overrideModel and decides whether the embedding alias is
// required. An empty allowedModels permits every model. An empty result becomes the deny-all placeholder,
// because LiteLLM treats an empty models list as unrestricted.
func gatewayWhitelist(
	ctx context.Context,
	db *gorm.DB,
	allowedModels []string,
	instance *rentity.TenantModelInstance,
	overrideModel string,
	overrideType int,
	overrideMode gatewayOverride,
) ([]string, error) {
	isPermitted := func(model string) bool {
		return len(allowedModels) == 0 || isModelPermitted(model, allowedModels)
	}

	var activeModels []rentity.TenantModel
	if err := db.WithContext(ctx).Where("instance_id = ? AND status = ?", instance.ID, "active").
		Find(&activeModels).Error; err != nil {
		return nil, fmt.Errorf("failed to query active models for gateway key update: %w", err)
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
	return whitelist, nil
}

// rebuildGatewayWhitelist rewrites the tenant virtual key whitelist from the active tenant_model rows
// of the instance. LiteLLM /key/update replaces the whole models list, so the full set is always sent.
// The caller must hold the instance whitelist lock.
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
	whitelist, err := gatewayWhitelist(ctx, db, allowedModels, instance, overrideModel, overrideType, overrideMode)
	if err != nil {
		return err
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
