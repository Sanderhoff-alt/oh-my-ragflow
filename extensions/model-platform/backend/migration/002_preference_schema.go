package migration

import (
	"context"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// MigratePreferenceSchema applies database migrations for platform_user_default_preference table.
// It is idempotent across MySQL, PostgreSQL, and SQLite.
func MigratePreferenceSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}

	return db.WithContext(ctx).AutoMigrate(&entity.UserDefaultPreference{})
}
