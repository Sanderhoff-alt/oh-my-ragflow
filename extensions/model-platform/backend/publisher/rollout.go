package publisher

import (
	"context"
	"errors"
	"fmt"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrModelNotFound reports a catalog model id that does not exist.
	ErrModelNotFound = errors.New("model not found in the catalog")
	// ErrRetireDefault reports an attempt to retire the platform default of a model type.
	ErrRetireDefault = errors.New("model is the platform default")
	// ErrModelNotActive reports an action that needs an active catalog model.
	ErrModelNotActive = errors.New("model is not active")
)

// RetireDefaultError names the default model whose retirement was refused.
type RetireDefaultError struct{ ModelName string }

func (e *RetireDefaultError) Error() string {
	return fmt.Sprintf("Cannot retire model '%s': it is currently designated as the default model. "+
		"Please set another active model as default first.", e.ModelName)
}

// Is makes errors.Is(err, ErrRetireDefault) match.
func (e *RetireDefaultError) Is(target error) bool { return target == ErrRetireDefault }

// unflagged matches a tenant_model row that carries neither retirement flag.
const unflagged = `COALESCE(m.extra, '') NOT LIKE '%"tombstoned":true%' AND COALESCE(m.extra, '') NOT LIKE '%"deprecated":true%'`

// startRollout records a task for the model and an item for every tenant that must change to match the
// catalog intent. The task is only a progress view: the worker converges tenants from the catalog status.
func startRollout(tx *gorm.DB, model *entity.PlatformManagedModel) (*Result, error) {
	return startRolloutFor(tx, model, tenantsToConverge)
}

// startRolloutFor is startRollout with an explicit choice of the tenants that get an item.
func startRolloutFor(
	tx *gorm.DB,
	model *entity.PlatformManagedModel,
	selectTenants func(db *gorm.DB, model *entity.PlatformManagedModel, taskID string) ([]string, error),
) (*Result, error) {
	task := entity.ModelPublishTask{ID: newID(), ModelName: model.ModelName, ModelType: model.ModelType}
	if err := tx.Create(&task).Error; err != nil {
		return nil, err
	}
	tenants, err := selectTenants(tx, model, task.ID)
	if err != nil {
		return nil, err
	}
	if err := insertItems(tx, task.ID, tenants); err != nil {
		return nil, err
	}
	return &Result{TaskID: task.ID, Total: len(tenants)}, nil
}

// readyTenants returns every Ready tenant that has no item in taskID, whatever its model state is.
func readyTenants(db *gorm.DB, _ *entity.PlatformManagedModel, taskID string) ([]string, error) {
	var tenants []string
	err := db.Raw(`
SELECT r.tenant_id FROM platform_tenant_reconcile r
WHERE r.status = ?
AND NOT EXISTS (
	SELECT 1 FROM platform_model_publish_item it WHERE it.task_id = ? AND it.tenant_id = r.tenant_id
)
ORDER BY r.tenant_id`, entity.StatusReady, taskID).Scan(&tenants).Error
	return tenants, err
}

// SetDefault makes an active catalog model the platform default of its type and starts the rollout that
// moves every tenant's platform_default users to it. Users who chose a model themselves (user_explicit,
// see #49) are preserved. The previous default stays active.
func SetDefault(ctx context.Context, db *gorm.DB, modelID string) (*Result, error) {
	var result *Result
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockCatalogModel(tx, modelID)
		if err != nil {
			return err
		}
		if model.Status != entity.ManagedModelStatusActive {
			return fmt.Errorf("%w: %q is %s, reactivate it before making it the default", ErrModelNotActive, model.ModelName, model.Status)
		}
		if err := tx.Model(&entity.PlatformManagedModel{}).
			Where("model_type = ? AND id <> ? AND is_default = ?", model.ModelType, model.ID, true).
			Update("is_default", false).Error; err != nil {
			return err
		}
		if err := tx.Model(&entity.PlatformManagedModel{}).Where("id = ?", model.ID).
			Update("is_default", true).Error; err != nil {
			return err
		}
		model.IsDefault = true
		result, err = startRolloutFor(tx, model, readyTenants)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// tenantsToConverge returns the tenants whose realized state differs from the catalog intent and that have
// no item in taskID yet.
//   - active: Ready tenants without a healthy row, which covers missing rows and rows retired earlier. A row
//     disabled by someone else (no retirement flag) counts as present, so it is never resurrected.
//   - retired: tenants that still have a fully active row. Deprecated rows are already converged.
func tenantsToConverge(db *gorm.DB, model *entity.PlatformManagedModel, taskID string) ([]string, error) {
	var tenants []string
	if model.Status == entity.ManagedModelStatusRetired {
		err := db.Raw(`
SELECT p.tenant_id FROM tenant_model_provider p
JOIN tenant_model_instance i ON i.provider_id = p.id AND i.instance_name = ?
JOIN tenant_model m ON m.instance_id = i.id AND m.model_name = ? AND m.model_type = ?
WHERE p.provider_name = ? AND m.status = 'active' AND `+unflagged+`
AND NOT EXISTS (
	SELECT 1 FROM platform_model_publish_item it WHERE it.task_id = ? AND it.tenant_id = p.tenant_id
)
ORDER BY p.tenant_id`,
			reconciler.PlatformInstanceName, model.ModelName, model.ModelType,
			reconciler.ProviderOpenAIAPICompatible, taskID,
		).Scan(&tenants).Error
		return tenants, err
	}

	err := db.Raw(`
SELECT r.tenant_id FROM platform_tenant_reconcile r
WHERE r.status = ?
AND NOT EXISTS (
	SELECT 1 FROM tenant_model_provider p
	JOIN tenant_model_instance i ON i.provider_id = p.id AND i.instance_name = ?
	JOIN tenant_model m ON m.instance_id = i.id AND m.model_name = ? AND m.model_type = ?
	WHERE p.tenant_id = r.tenant_id AND p.provider_name = ? AND `+unflagged+`
)
AND NOT EXISTS (
	SELECT 1 FROM platform_model_publish_item it WHERE it.task_id = ? AND it.tenant_id = r.tenant_id
)
ORDER BY r.tenant_id`,
		entity.StatusReady, reconciler.PlatformInstanceName, model.ModelName, model.ModelType,
		reconciler.ProviderOpenAIAPICompatible, taskID,
	).Scan(&tenants).Error
	return tenants, err
}

// lockCatalogModel loads a catalog row and, outside SQLite, locks it so retire, reactivate and set-default
// on the same model are serialized.
func lockCatalogModel(tx *gorm.DB, modelID string) (*entity.PlatformManagedModel, error) {
	query := tx
	if tx.Dialector.Name() != "sqlite" {
		query = tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var models []entity.PlatformManagedModel
	if err := query.Where("id = ?", modelID).Limit(1).Find(&models).Error; err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, ErrModelNotFound
	}
	return &models[0], nil
}

// Retire marks a catalog model retired and starts the rollout that converges every tenant: embedding models
// become deprecated or tombstoned depending on the tenant's own knowledge bases, other models are
// tombstoned. The platform default of a type cannot be retired: promote another model first.
func Retire(ctx context.Context, db *gorm.DB, modelID string) (*Result, error) {
	var result *Result
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockCatalogModel(tx, modelID)
		if err != nil {
			return err
		}
		if model.IsDefault {
			return &RetireDefaultError{ModelName: model.ModelName}
		}
		model.Status = entity.ManagedModelStatusRetired
		if err := tx.Model(&entity.PlatformManagedModel{}).Where("id = ?", model.ID).
			Update("status", model.Status).Error; err != nil {
			return err
		}
		if result, err = startRollout(tx, model); err != nil {
			return err
		}
		result.AffectedUsers, err = countPinnedUsers(tx, model)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Reactivate marks a retired catalog model active again and starts the rollout that brings tenants back:
// the whitelist is restored before each tenant row. Reactivating an active model only repairs drift.
func Reactivate(ctx context.Context, db *gorm.DB, modelID string) (*Result, error) {
	var result *Result
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := lockCatalogModel(tx, modelID)
		if err != nil {
			return err
		}
		model.Status = entity.ManagedModelStatusActive
		if err := tx.Model(&entity.PlatformManagedModel{}).Where("id = ?", model.ID).
			Update("status", model.Status).Error; err != nil {
			return err
		}
		result, err = startRollout(tx, model)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// countPinnedUsers counts the users who explicitly chose the model as their default. A retirement never
// overwrites such a choice, so the administrator needs to know how many users it will affect.
func countPinnedUsers(tx *gorm.DB, model *entity.PlatformManagedModel) (int64, error) {
	if !tx.Migrator().HasTable(&entity.UserDefaultPreference{}) {
		return 0, nil
	}
	var count int64
	err := tx.Model(&entity.UserDefaultPreference{}).
		Where("origin = ? AND model_type = ? AND current_model_id IN (?, ?)",
			entity.OriginUserExplicit, model.ModelType, model.ModelName,
			model.ModelName+"@"+reconciler.PlatformInstanceName+"@"+reconciler.ProviderOpenAIAPICompatible).
		Count(&count).Error
	return count, err
}
