package reconciler

import (
	"context"
	"sort"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func clearCatalog(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Where("1 = 1").Delete(&entity.PlatformManagedModel{}).Error; err != nil {
		t.Fatalf("failed to clear the catalog: %v", err)
	}
}

func lastGeneratedModels(m *mockGatewayClient) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, req := range m.generated {
		return append([]string(nil), req.Models...)
	}
	return nil
}

func TestTenantReconciler_EmptyCatalogColdStart(t *testing.T) {
	db := setupTestDB(t)
	clearCatalog(t, db)
	mockClient := newMockGatewayClient()
	createBareTestTenant(db, "tenant-cold")
	createTestUserTenantInDB(db, "user-cold", "tenant-cold", "", "")

	if err := NewTenantReconciler(mockClient, "", nil).Reconcile(context.Background(), db, "tenant-cold"); err != nil {
		t.Fatalf("an empty catalog must not fail the tenant: %v", err)
	}

	if models := lastGeneratedModels(mockClient); len(models) != 1 || models[0] != RevokedModelPlaceholder {
		t.Errorf("expected the deny-all placeholder instead of an empty list, got %v", models)
	}
	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ?", "tenant-cold").First(&provider).Error; err != nil {
		t.Fatalf("expected the managed provider to exist for the fan-out worker: %v", err)
	}
	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error; err != nil {
		t.Fatalf("expected the managed instance to exist for the fan-out worker: %v", err)
	}
	var models int64
	db.Model(&rentity.TenantModel{}).Where("instance_id = ?", instance.ID).Count(&models)
	if models != 0 {
		t.Errorf("expected no tenant models without a catalog, got %d", models)
	}
	var ut rentity.UserTenant
	db.Where("user_id = ?", "user-cold").First(&ut)
	if ut.LLMID != "" || ut.EmbdID != "" {
		t.Errorf("expected no default models without a catalog, got llm=%q embd=%q", ut.LLMID, ut.EmbdID)
	}
}

func TestTenantReconciler_ProvisionsFromCatalogAndHonorsCatalogDefaults(t *testing.T) {
	db := setupTestDB(t)
	clearCatalog(t, db)
	chat := int(rentity.ModelTypeChat)
	rerank := int(rentity.ModelTypeRerank)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: chat, IsDefault: true, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m2", ModelName: "qwen-max", ModelType: chat, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m3", ModelName: "bge-reranker", ModelType: rerank, IsDefault: true, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m4", ModelName: "retired-model", ModelType: chat, Status: entity.ManagedModelStatusRetired})
	mockClient := newMockGatewayClient()
	createBareTestTenant(db, "tenant-catalog")
	createTestUserTenantInDB(db, "user-catalog", "tenant-catalog", "", "")

	if err := NewTenantReconciler(mockClient, "", nil).Reconcile(context.Background(), db, "tenant-catalog"); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	got := lastGeneratedModels(mockClient)
	sort.Strings(got)
	want := []string{"bge-reranker", "deepseek-v3", "platform/chat", "qwen-max"}
	if len(got) != len(want) {
		t.Fatalf("expected whitelist %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected whitelist %v, got %v", want, got)
		}
	}

	var ut rentity.UserTenant
	db.Where("user_id = ?", "user-catalog").First(&ut)
	if ut.LLMID != "deepseek-v3@platform@OpenAI-API-Compatible" || ut.RerankID != "bge-reranker@platform@OpenAI-API-Compatible" {
		t.Errorf("expected the catalog defaults, got llm=%q rerank=%q", ut.LLMID, ut.RerankID)
	}
	var retired int64
	db.Model(&rentity.TenantModel{}).Where("model_name = ?", "retired-model").Count(&retired)
	if retired != 0 {
		t.Error("a retired catalog model must not be provisioned to a new tenant")
	}
}

func TestTenantReconciler_AllowedModelsFilterTheCatalog(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	createBareTestTenant(db, "tenant-filter")

	if err := NewTenantReconciler(mockClient, "", []string{DefaultChatModelName}).Reconcile(context.Background(), db, "tenant-filter"); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var names []string
	db.Model(&rentity.TenantModel{}).Pluck("model_name", &names)
	if len(names) != 1 || names[0] != DefaultChatModelName {
		t.Errorf("expected only the permitted catalog model, got %v", names)
	}
}
