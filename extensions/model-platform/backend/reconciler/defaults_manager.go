package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultSlot aliases entity.DefaultSlot for callers building DefaultModelConfig maps.
type DefaultSlot = mpentity.DefaultSlot

// DefaultModelConfig maps each managed ModelType to its recommended default model slot.
type DefaultModelConfig map[rentity.ModelType]DefaultSlot

// DefaultsInitResult records the number of user_tenant rows updated per managed ModelType.
type DefaultsInitResult map[rentity.ModelType]int64

// DefaultsManager handles atomic and safe initialization of tenant default models.
// It uses conditional atomic SQL to guarantee that user-explicit custom selections
// are 100% protected against accidental overwrites by background reconcilers.
type DefaultsManager struct {
	stickyGuard *StickyGuard
}

// NewDefaultsManager creates a new DefaultsManager.
func NewDefaultsManager() *DefaultsManager {
	return &DefaultsManager{
		stickyGuard: NewStickyGuard(),
	}
}

// InitializeTenantDefaults conditionally initializes default models for all users in a tenant.
// It executes atomic UPDATE statements checking for empty or NULL columns,
// ensuring zero rows are touched if the user already configured a model.
func (m *DefaultsManager) InitializeTenantDefaults(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	cfg DefaultModelConfig,
) (DefaultsInitResult, error) {
	return m.InitializeUserDefaults(ctx, db, "", tenantID, cfg)
}

// InitializeMemberFromOwner conditionally initializes a joining member's empty model defaults
// by inheriting from the tenant owner's configuration. Existing user choices are preserved.
func (m *DefaultsManager) InitializeMemberFromOwner(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
) (DefaultsInitResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if userID == "" {
		return nil, errors.New("defaults manager requires a non-empty userID")
	}

	var owner rentity.UserTenant
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND role = ?", tenantID, "owner").
		First(&owner).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DefaultsInitResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lookup tenant owner for tenant %s: %w", tenantID, err)
	}

	cfg := make(DefaultModelConfig, len(mpentity.ManagedModelSlots))
	for _, spec := range mpentity.ManagedModelSlots {
		slot := spec.Read(&owner)
		if slot.ModelID != "" {
			cfg[spec.Type] = slot
		}
	}

	return m.InitializeUserDefaults(ctx, db, userID, tenantID, cfg)
}

func (m *DefaultsManager) recordModelDefaults(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
	modelType int,
	modelID string,
	colName string,
) error {
	if m.stickyGuard == nil || modelID == "" {
		return nil
	}
	if userID != "" {
		return m.stickyGuard.RecordPlatformDefault(ctx, db, tenantID, userID, modelType, modelID)
	}
	var members []rentity.UserTenant
	if err := db.Where(fmt.Sprintf("tenant_id = ? AND %s = ?", colName), tenantID, modelID).Find(&members).Error; err != nil {
		return fmt.Errorf("failed to query members for %s provenance: %w", colName, err)
	}
	for _, mem := range members {
		if err := m.stickyGuard.RecordPlatformDefault(ctx, db, tenantID, mem.UserID, modelType, modelID); err != nil {
			return fmt.Errorf("failed to record default preference for user %s: %w", mem.UserID, err)
		}
	}
	return nil
}

// InitializeUserDefaults conditionally initializes default models for a specific user (or all users if userID is empty)
// in a tenant. It protects existing user choices with atomic conditional SQL.
func (m *DefaultsManager) InitializeUserDefaults(
	ctx context.Context,
	db *gorm.DB,
	userID string,
	tenantID string,
	cfg DefaultModelConfig,
) (DefaultsInitResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	result := make(DefaultsInitResult, len(cfg))

	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	applyUserScope := func(tx *gorm.DB) *gorm.DB {
		tx = tx.Where("tenant_id = ?", tenantID)
		if userID != "" {
			tx = tx.Where("user_id = ?", userID)
		}
		return tx
	}

	for _, spec := range mpentity.ManagedModelSlots {
		slot, ok := cfg[spec.Type]
		if !ok || slot.ModelID == "" {
			continue
		}

		updates := map[string]interface{}{
			spec.IDColumn: slot.ModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if slot.TenantModelID != "" {
			tid := slot.TenantModelID
			updates[spec.TenantIDColumn] = &tid
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where(fmt.Sprintf("(%s IS NULL OR %s = '')", spec.IDColumn, spec.IDColumn)).
			Updates(updates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize %s default model: %w", spec.Name, res.Error)
		}
		result[spec.Type] = res.RowsAffected

		if res.RowsAffected > 0 {
			if err := m.recordModelDefaults(ctx, silentDB, tenantID, userID, int(spec.Type), slot.ModelID, spec.IDColumn); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}
