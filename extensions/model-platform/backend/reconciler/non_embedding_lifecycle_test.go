package reconciler

import (
	"context"
	"errors"
	"sort"
	"testing"

	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

const nonEmbTenant = "tenant-nonemb"

// seedNonEmbeddingTenant creates a managed instance holding deepseek-v3 (chat, active), plus a tenant-owned
// instance with a model of the same name that the lifecycle must never touch.
func seedNonEmbeddingTenant(t *testing.T) (*gorm.DB, *rentity.TenantModelInstance) {
	t.Helper()
	db := setupTestDB(t)
	provider := rentity.TenantModelProvider{ID: "prov-ne", TenantID: nonEmbTenant, ProviderName: ProviderOpenAIAPICompatible}
	managed := rentity.TenantModelInstance{ID: "inst-ne", InstanceName: PlatformInstanceName, ProviderID: provider.ID, APIKey: "sk-ne", Status: "active"}
	own := rentity.TenantModelInstance{ID: "inst-own", InstanceName: "my-own", ProviderID: provider.ID, APIKey: "sk-own", Status: "active"}
	rows := []interface{}{
		&provider, &managed, &own,
		&rentity.TenantModel{ID: "tm-managed", ModelName: "deepseek-v3", ProviderID: provider.ID, InstanceID: managed.ID, ModelType: int(rentity.ModelTypeChat), Status: "active", Extra: `{"is_managed":true}`},
		&rentity.TenantModel{ID: "tm-other", ModelName: "qwen-max", ProviderID: provider.ID, InstanceID: managed.ID, ModelType: int(rentity.ModelTypeChat), Status: "active", Extra: `{"is_managed":true}`},
		&rentity.TenantModel{ID: "tm-own", ModelName: "deepseek-v3", ProviderID: provider.ID, InstanceID: own.ID, ModelType: int(rentity.ModelTypeChat), Status: "active", Extra: `{}`},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("failed to seed: %v", err)
		}
	}
	return db, &managed
}

func sortedModels(req []string) []string {
	out := append([]string(nil), req...)
	sort.Strings(out)
	return out
}

func TestNonEmbeddingLifecycle_RetireTombstonesAndRemovesFromWhitelist(t *testing.T) {
	db, _ := seedNonEmbeddingTenant(t)
	client := newMockGatewayClient()
	mgr := NewNonEmbeddingLifecycleManager(client)
	chat := int(rentity.ModelTypeChat)

	changed, err := mgr.RetireModel(context.Background(), db, nonEmbTenant, "deepseek-v3", chat)
	if err != nil || !changed {
		t.Fatalf("expected a transition, got changed=%v err=%v", changed, err)
	}

	got := sortedModels(client.lastUpdateKeyReq.Models)
	if len(got) != 2 || got[0] != "platform/chat" || got[1] != "qwen-max" {
		t.Errorf("expected the retired model to leave the whitelist and the others to stay, got %v", got)
	}
	var tm rentity.TenantModel
	db.Where("id = ?", "tm-managed").First(&tm)
	deprecated, tombstoned := RetirementFlags(tm.Extra)
	if tm.Status != "disabled" || !tombstoned || deprecated {
		t.Errorf("expected a tombstoned, disabled row without the deprecated flag, got %+v", tm)
	}
	var own rentity.TenantModel
	db.Where("id = ?", "tm-own").First(&own)
	if own.Status != "active" {
		t.Error("a tenant's own model with the same name must never be retired")
	}

	calls := len(client.updateHistory)
	changed, err = mgr.RetireModel(context.Background(), db, nonEmbTenant, "deepseek-v3", chat)
	if err != nil || changed || len(client.updateHistory) != calls {
		t.Errorf("a second retire must be a no-op without gateway calls, got changed=%v err=%v calls=%d->%d", changed, err, calls, len(client.updateHistory))
	}
}

func TestNonEmbeddingLifecycle_ReactivateRestoresWhitelistAndRow(t *testing.T) {
	db, _ := seedNonEmbeddingTenant(t)
	client := newMockGatewayClient()
	mgr := NewNonEmbeddingLifecycleManager(client)
	chat := int(rentity.ModelTypeChat)
	ctx := context.Background()

	if _, err := mgr.RetireModel(ctx, db, nonEmbTenant, "deepseek-v3", chat); err != nil {
		t.Fatalf("retire failed: %v", err)
	}
	changed, err := mgr.ReactivateModel(ctx, db, nonEmbTenant, "deepseek-v3", chat)
	if err != nil || !changed {
		t.Fatalf("expected a transition, got changed=%v err=%v", changed, err)
	}

	got := sortedModels(client.lastUpdateKeyReq.Models)
	if len(got) != 3 || got[0] != "deepseek-v3" {
		t.Errorf("expected the model back in the whitelist, got %v", got)
	}
	var tm rentity.TenantModel
	db.Where("id = ?", "tm-managed").First(&tm)
	deprecated, tombstoned := RetirementFlags(tm.Extra)
	if tm.Status != "active" || deprecated || tombstoned {
		t.Errorf("expected an active row without retirement flags, got %+v", tm)
	}
	if changed, _ := mgr.ReactivateModel(ctx, db, nonEmbTenant, "deepseek-v3", chat); changed {
		t.Error("reactivating an active model must be a no-op")
	}
}

func TestNonEmbeddingLifecycle_GatewayFailureLeavesTheRowUntouched(t *testing.T) {
	db, _ := seedNonEmbeddingTenant(t)
	client := newMockGatewayClient()
	client.updateErr = errors.New("gateway down")
	mgr := NewNonEmbeddingLifecycleManager(client)

	if _, err := mgr.RetireModel(context.Background(), db, nonEmbTenant, "deepseek-v3", int(rentity.ModelTypeChat)); err == nil {
		t.Fatal("expected the gateway failure to surface so the item is retried")
	}
	var tm rentity.TenantModel
	db.Where("id = ?", "tm-managed").First(&tm)
	if tm.Status != "active" || tm.Extra != `{"is_managed":true}` {
		t.Errorf("the row must not change before the gateway accepted the whitelist, got %+v", tm)
	}
}

func TestNonEmbeddingLifecycle_MissingModelIsANoOp(t *testing.T) {
	db, _ := seedNonEmbeddingTenant(t)
	mgr := NewNonEmbeddingLifecycleManager(newMockGatewayClient())

	changed, err := mgr.RetireModel(context.Background(), db, nonEmbTenant, "never-onboarded", int(rentity.ModelTypeChat))
	if err != nil || changed {
		t.Errorf("expected a no-op for a model the tenant never had, got changed=%v err=%v", changed, err)
	}
}
