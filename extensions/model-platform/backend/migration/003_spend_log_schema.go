package migration

import (
	"context"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// MigrateSpendLogSchema applies database migrations for platform_gateway_spend_log table.
// It is idempotent across MySQL, PostgreSQL, and SQLite.
func MigrateSpendLogSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}

	return db.WithContext(ctx).AutoMigrate(&entity.GatewaySpendLog{})
}
