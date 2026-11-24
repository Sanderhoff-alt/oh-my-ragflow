package migration

import (
	"context"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// MigratePublishSchema applies database migrations for the managed model catalog and the
// tenant fan-out publish task and item tables. It is idempotent across MySQL, PostgreSQL, and SQLite.
func MigratePublishSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}

	return db.WithContext(ctx).AutoMigrate(
		&entity.PlatformManagedModel{},
		&entity.ModelPublishTask{},
		&entity.ModelPublishItem{},
	)
}
