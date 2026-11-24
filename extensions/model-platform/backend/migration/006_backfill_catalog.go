package migration

import (
	"context"
	"strings"

	"ragflow/extensions/model-platform/backend/entity"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// legacyDefaultModels are the models older deployments provisioned for every tenant before the catalog
// existed (chat, then embedding). They keep being the platform default of their type after the backfill so
// new tenants still get the same initial defaults.
var legacyDefaultModels = []struct {
	name      string
	modelType int
}{
	{"gpt-4o-mini", 1},
	{"text-embedding-3-small", 2},
}

// BackfillManagedCatalog adopts the models that existing tenants already use into platform_managed_model.
// It reads the managed platform instances only, never changes tenant rows, and is idempotent: existing
// catalog rows are kept and a type that already has a default keeps it.
func BackfillManagedCatalog(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrNilDB
	}
	db = db.WithContext(ctx)

	migrator := db.Migrator()
	for _, table := range []string{"tenant_model", "tenant_model_instance", "tenant_model_provider"} {
		if !migrator.HasTable(table) {
			return nil
		}
	}

	var adopted []struct {
		ModelName   string
		ModelType   int
		ActiveCount int64
	}
	if err := db.Raw(`
SELECT m.model_name AS model_name, m.model_type AS model_type,
	SUM(CASE WHEN m.status = 'active' THEN 1 ELSE 0 END) AS active_count
FROM tenant_model m
JOIN tenant_model_instance i ON i.id = m.instance_id AND i.instance_name = 'platform'
JOIN tenant_model_provider p ON p.id = i.provider_id AND p.provider_name = 'OpenAI-API-Compatible'
GROUP BY m.model_name, m.model_type`).Scan(&adopted).Error; err != nil {
		return err
	}

	for _, a := range adopted {
		status := entity.ManagedModelStatusRetired
		if a.ActiveCount > 0 {
			status = entity.ManagedModelStatusActive
		}
		row := entity.PlatformManagedModel{
			ID:        strings.ReplaceAll(uuid.New().String(), "-", ""),
			ModelName: a.ModelName,
			ModelType: a.ModelType,
			Status:    status,
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return err
		}
	}

	for _, legacy := range legacyDefaultModels {
		var defaults int64
		if err := db.Model(&entity.PlatformManagedModel{}).
			Where("model_type = ? AND is_default = ?", legacy.modelType, true).Count(&defaults).Error; err != nil {
			return err
		}
		if defaults > 0 {
			continue
		}
		if err := db.Model(&entity.PlatformManagedModel{}).
			Where("model_name = ? AND model_type = ? AND status = ?", legacy.name, legacy.modelType, entity.ManagedModelStatusActive).
			Update("is_default", true).Error; err != nil {
			return err
		}
	}
	return nil
}
