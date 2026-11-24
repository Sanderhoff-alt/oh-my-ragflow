package migration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupBackfillDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:backfill_test_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := MigratePublishSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := db.AutoMigrate(&rentity.TenantModelProvider{}, &rentity.TenantModelInstance{}, &rentity.TenantModel{}); err != nil {
		t.Fatalf("failed to migrate tenant model tables: %v", err)
	}
	return db
}

func seedInstance(t *testing.T, db *gorm.DB, tenantID, instanceName string, models map[string]struct {
	modelType int
	status    string
}) {
	t.Helper()
	provider := rentity.TenantModelProvider{ID: "prov-" + tenantID, TenantID: tenantID, ProviderName: "OpenAI-API-Compatible"}
	instance := rentity.TenantModelInstance{ID: "inst-" + tenantID + instanceName, InstanceName: instanceName, ProviderID: provider.ID, APIKey: "sk", Status: "active"}
	if err := db.FirstOrCreate(&provider, "id = ?", provider.ID).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	for name, m := range models {
		tm := rentity.TenantModel{ID: "tm-" + tenantID + instanceName + name, ModelName: name, ProviderID: provider.ID, InstanceID: instance.ID, ModelType: m.modelType, Status: m.status}
		if err := db.Create(&tm).Error; err != nil {
			t.Fatalf("seed model: %v", err)
		}
	}
}

type seededModel = struct {
	modelType int
	status    string
}

func TestBackfillManagedCatalog_AdoptsManagedModelsAndLegacyDefaults(t *testing.T) {
	db := setupBackfillDB(t)
	seedInstance(t, db, "t1", "platform", map[string]seededModel{
		"gpt-4o-mini":            {1, "active"},
		"text-embedding-3-small": {2, "active"},
		"old-model":              {1, "disabled"},
	})
	seedInstance(t, db, "t2", "platform", map[string]seededModel{"gpt-4o-mini": {1, "active"}})
	seedInstance(t, db, "t3", "my-own", map[string]seededModel{"byok-model": {1, "active"}})

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := BackfillManagedCatalog(ctx, db); err != nil {
			t.Fatalf("backfill run %d failed: %v", i+1, err)
		}
	}

	var rows []entity.PlatformManagedModel
	db.Order("model_name ASC").Find(&rows)
	got := map[string]entity.PlatformManagedModel{}
	for _, r := range rows {
		got[r.ModelName] = r
	}
	if len(rows) != 3 {
		t.Fatalf("expected exactly the 3 managed models to be adopted, got %d: %+v", len(rows), rows)
	}
	if _, ok := got["byok-model"]; ok {
		t.Error("a tenant's own instance must never be adopted into the catalog")
	}
	if got["gpt-4o-mini"].Status != entity.ManagedModelStatusActive || !got["gpt-4o-mini"].IsDefault {
		t.Errorf("legacy chat default must be adopted as the active default, got %+v", got["gpt-4o-mini"])
	}
	if got["text-embedding-3-small"].Status != entity.ManagedModelStatusActive || !got["text-embedding-3-small"].IsDefault {
		t.Errorf("legacy embedding default must be adopted as the active default, got %+v", got["text-embedding-3-small"])
	}
	if got["old-model"].Status != entity.ManagedModelStatusRetired || got["old-model"].IsDefault {
		t.Errorf("a model without any active tenant row must be adopted as retired, got %+v", got["old-model"])
	}
}

func TestBackfillManagedCatalog_KeepsAnExistingDefault(t *testing.T) {
	db := setupBackfillDB(t)
	seedInstance(t, db, "t1", "platform", map[string]seededModel{
		"gpt-4o-mini": {1, "active"},
		"deepseek-v3": {1, "active"},
	})
	db.Create(&entity.PlatformManagedModel{ID: "m-ds", ModelName: "deepseek-v3", ModelType: 1, IsDefault: true, Status: entity.ManagedModelStatusActive})

	if err := BackfillManagedCatalog(context.Background(), db); err != nil {
		t.Fatalf("backfill failed: %v", err)
	}

	var legacy entity.PlatformManagedModel
	db.Where("model_name = ?", "gpt-4o-mini").First(&legacy)
	if legacy.IsDefault {
		t.Error("the legacy default must not replace a default the administrator already chose")
	}
}

func TestBackfillManagedCatalog_SkipsWhenTenantTablesAreMissing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:backfill_empty_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := MigratePublishSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := BackfillManagedCatalog(context.Background(), db); err != nil {
		t.Errorf("expected a no-op without tenant tables, got %v", err)
	}
	if err := BackfillManagedCatalog(context.Background(), nil); err != ErrNilDB {
		t.Errorf("expected ErrNilDB, got %v", err)
	}
}
