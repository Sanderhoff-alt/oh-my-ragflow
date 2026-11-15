package migration

import (
	"context"
	"errors"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

var (
	// ErrNilDB indicates a nil *gorm.DB was passed to migration.
	ErrNilDB = errors.New("migration requires a valid *gorm.DB instance")
)

// MigrateReconcileSchema applies database migrations for model platform reconcile schema.
// It is idempotent across MySQL, PostgreSQL, and SQLite.
func MigrateReconcileSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}

	// Auto-migrate the platform_tenant_reconcile table and indexes
	if err := db.WithContext(ctx).AutoMigrate(&entity.TenantReconcileRecord{}); err != nil {
		return err
	}

	return nil
}
