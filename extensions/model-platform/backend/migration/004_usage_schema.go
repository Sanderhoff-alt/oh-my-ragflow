package migration

import (
	"context"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// MigrateUsageSchema applies database migrations for platform_tenant_usage_daily table.
// It is idempotent across MySQL, PostgreSQL, and SQLite.
func MigrateUsageSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}

	return db.WithContext(ctx).AutoMigrate(&entity.TenantUsageDaily{})
}
