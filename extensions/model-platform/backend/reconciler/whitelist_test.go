package reconciler

import (
	"context"
	"sort"
	"testing"

	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func seedWhitelistInstance(t *testing.T, models map[string]rentity.ModelType) (*gorm.DB, *rentity.TenantModelInstance) {
	t.Helper()
	db := setupTestDB(t)
	instance := &rentity.TenantModelInstance{ID: "inst-wl", InstanceName: PlatformInstanceName, ProviderID: "prov-wl", APIKey: "sk-wl", Status: "active"}
	if err := db.Create(instance).Error; err != nil {
		t.Fatalf("failed to seed instance: %v", err)
	}
	for name, mt := range models {
		tm := rentity.TenantModel{ID: "tm-" + name, ModelName: name, ProviderID: "prov-wl", InstanceID: instance.ID, ModelType: int(mt), Status: "active"}
		if err := db.Create(&tm).Error; err != nil {
			t.Fatalf("failed to seed model: %v", err)
		}
	}
	return db, instance
}

func TestIncludeModelInWhitelist_KeepsExistingModelsAndSkipsEmbeddingAliasForChat(t *testing.T) {
	db, instance := seedWhitelistInstance(t, map[string]rentity.ModelType{"gpt-4o-mini": rentity.ModelTypeChat})
	client := newMockGatewayClient()

	if err := IncludeModelInWhitelist(context.Background(), db, client, instance, "deepseek-v3", int(rentity.ModelTypeChat)); err != nil {
		t.Fatalf("IncludeModelInWhitelist failed: %v", err)
	}

	got := append([]string(nil), client.lastUpdateKeyReq.Models...)
	sort.Strings(got)
	want := []string{"deepseek-v3", "gpt-4o-mini", "platform/chat"}
	if len(got) != len(want) {
		t.Fatalf("expected whitelist %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected whitelist %v, got %v", want, got)
		}
	}
	if client.lastUpdateKeyReq.Key != "sk-wl" {
		t.Errorf("expected the instance virtual key to be updated, got %s", client.lastUpdateKeyReq.Key)
	}
}

func TestIncludeModelInWhitelist_EmbeddingAddsEmbeddingAlias(t *testing.T) {
	db, instance := seedWhitelistInstance(t, map[string]rentity.ModelType{"gpt-4o-mini": rentity.ModelTypeChat})
	client := newMockGatewayClient()

	if err := IncludeModelInWhitelist(context.Background(), db, client, instance, "bge-m3", int(rentity.ModelTypeEmbedding)); err != nil {
		t.Fatalf("IncludeModelInWhitelist failed: %v", err)
	}

	found := false
	for _, m := range client.lastUpdateKeyReq.Models {
		if m == "platform/embedding" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected platform/embedding alias for an embedding model, got %v", client.lastUpdateKeyReq.Models)
	}
}
