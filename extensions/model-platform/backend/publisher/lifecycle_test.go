package publisher

import (
	"context"
	"errors"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

// publishAndConverge onboards deepseek-v3 (chat) to the tenants and runs the worker until they have it.
func publishAndConverge(t *testing.T, db *gorm.DB, gw *fakeGateway, w *Worker) entity.PlatformManagedModel {
	t.Helper()
	publishDeepseek(t, db, gw)
	w.RunOnce(context.Background())
	var model entity.PlatformManagedModel
	if err := db.Where("model_name = ?", "deepseek-v3").First(&model).Error; err != nil {
		t.Fatalf("catalog row missing: %v", err)
	}
	return model
}

func tenantRow(t *testing.T, db *gorm.DB, tenantID, modelName string) rentity.TenantModel {
	t.Helper()
	row, ok := hasModel(t, db, tenantID, modelName)
	if !ok {
		t.Fatalf("tenant %s has no %s row", tenantID, modelName)
	}
	return row
}

func TestRetireAndReactivate_ConvergeEveryTenantAndRestoreTheWhitelist(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "a", entity.StatusReady)
	seedTenant(t, db, "b", entity.StatusReady)
	w := newTestWorker(db, gw)
	model := publishAndConverge(t, db, gw, w)

	res, err := Retire(context.Background(), db, model.ID)
	if err != nil || res.Total != 2 {
		t.Fatalf("expected a retirement task for both tenants, got %+v (%v)", res, err)
	}
	w.RunOnce(context.Background())

	for _, id := range []string{"a", "b"} {
		row := tenantRow(t, db, id, "deepseek-v3")
		if _, tombstoned := reconciler.RetirementFlags(row.Extra); row.Status != "disabled" || !tombstoned {
			t.Errorf("tenant %s: expected a tombstoned row, got %+v", id, row)
		}
		for _, m := range gw.updates["sk-"+id] {
			if m == "deepseek-v3" {
				t.Errorf("tenant %s: the retired model must leave the whitelist, got %v", id, gw.updates["sk-"+id])
			}
		}
	}
	if p, _ := GetProgress(context.Background(), db, res.TaskID); p.Status != TaskStatusCompleted || p.Succeeded != 2 {
		t.Errorf("unexpected retirement progress %+v", p)
	}

	if _, err := Reactivate(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Reactivate failed: %v", err)
	}
	w.RunOnce(context.Background())

	for _, id := range []string{"a", "b"} {
		row := tenantRow(t, db, id, "deepseek-v3")
		if deprecated, tombstoned := reconciler.RetirementFlags(row.Extra); row.Status != "active" || deprecated || tombstoned {
			t.Errorf("tenant %s: expected an active row again, got %+v", id, row)
		}
		found := false
		for _, m := range gw.updates["sk-"+id] {
			found = found || m == "deepseek-v3"
		}
		if !found {
			t.Errorf("tenant %s: the reactivated model must be back in the whitelist, got %v", id, gw.updates["sk-"+id])
		}
	}
}

func TestRetire_RefusesThePlatformDefault(t *testing.T) {
	db := setupPublishDB(t)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat), IsDefault: true, Status: entity.ManagedModelStatusActive})

	_, err := Retire(context.Background(), db, "m1")
	if !errors.Is(err, ErrRetireDefault) {
		t.Fatalf("expected ErrRetireDefault, got %v", err)
	}
	want := "Cannot retire model 'deepseek-v3': it is currently designated as the default model. Please set another active model as default first."
	if err.Error() != want {
		t.Errorf("unexpected message %q", err.Error())
	}
	var model entity.PlatformManagedModel
	db.Where("id = ?", "m1").First(&model)
	if model.Status != entity.ManagedModelStatusActive {
		t.Errorf("a refused retirement must not change the catalog, got %s", model.Status)
	}
	if _, err := Retire(context.Background(), db, "missing"); !errors.Is(err, ErrModelNotFound) {
		t.Errorf("expected ErrModelNotFound, got %v", err)
	}
}

func TestRetire_EmbeddingIsDeprecatedWhereKnowledgeBasesRemainAndTombstonedElsewhere(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("bge-m3")
	seedTenant(t, db, "used", entity.StatusReady)
	seedTenant(t, db, "idle", entity.StatusReady)
	embedding := int(rentity.ModelTypeEmbedding)
	w := newTestWorker(db, gw)
	if _, err := New(gw).Publish(context.Background(), db, Request{ModelName: "bge-m3", ModelType: embedding}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	w.RunOnce(context.Background())
	db.Create(&rentity.Knowledgebase{ID: "kb1", TenantID: "used", Name: "kb1", EmbdID: "bge-m3@platform@OpenAI-API-Compatible"})
	var model entity.PlatformManagedModel
	db.Where("model_name = ?", "bge-m3").First(&model)

	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	w.RunOnce(context.Background())

	used := tenantRow(t, db, "used", "bge-m3")
	if deprecated, tombstoned := reconciler.RetirementFlags(used.Extra); used.Status != "active" || !deprecated || tombstoned {
		t.Errorf("a tenant with knowledge bases must keep a read-only deprecated row, got %+v", used)
	}
	idle := tenantRow(t, db, "idle", "bge-m3")
	if _, tombstoned := reconciler.RetirementFlags(idle.Extra); idle.Status != "disabled" || !tombstoned {
		t.Errorf("a tenant without knowledge bases must be tombstoned, got %+v", idle)
	}

	db.Model(&rentity.Knowledgebase{}).Where("id = ?", "kb1").Update("status", "0")
	w.RunOnce(context.Background())

	used = tenantRow(t, db, "used", "bge-m3")
	if _, tombstoned := reconciler.RetirementFlags(used.Extra); used.Status != "disabled" || !tombstoned {
		t.Errorf("the sweep must tombstone a deprecated row once its knowledge bases are gone, got %+v", used)
	}
}

func TestWorker_ChasesTheLatestCatalogIntent(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "a", entity.StatusReady)
	w := newTestWorker(db, gw)
	model := publishAndConverge(t, db, gw, w)

	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	if _, err := Reactivate(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Reactivate failed: %v", err)
	}
	callsBefore := gw.calls()
	w.RunOnce(context.Background())

	row := tenantRow(t, db, "a", "deepseek-v3")
	if row.Status != "active" || gw.calls() != callsBefore {
		t.Errorf("an item queued by an outdated retirement must follow the reactivation without touching the tenant, row=%+v calls=%d->%d", row, callsBefore, gw.calls())
	}
}

func TestRetire_ReportsUsersWhoPinnedTheModel(t *testing.T) {
	db := setupPublishDB(t)
	if err := db.AutoMigrate(&entity.UserDefaultPreference{}); err != nil {
		t.Fatalf("failed to migrate preferences: %v", err)
	}
	chat := int(rentity.ModelTypeChat)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: chat, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.UserDefaultPreference{ID: "p1", TenantID: "t1", UserID: "u1", ModelType: chat, Origin: entity.OriginUserExplicit, CurrentModelID: "deepseek-v3@platform@OpenAI-API-Compatible"})
	db.Create(&entity.UserDefaultPreference{ID: "p2", TenantID: "t1", UserID: "u2", ModelType: chat, Origin: entity.OriginPlatformDefault, CurrentModelID: "deepseek-v3@platform@OpenAI-API-Compatible"})
	db.Create(&entity.UserDefaultPreference{ID: "p3", TenantID: "t1", UserID: "u3", ModelType: chat, Origin: entity.OriginUserExplicit, CurrentModelID: "qwen-max@platform@OpenAI-API-Compatible"})

	res, err := Retire(context.Background(), db, "m1")
	if err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	if res.AffectedUsers != 1 {
		t.Errorf("expected only the user who pinned the model to be reported, got %d", res.AffectedUsers)
	}
}

func pinUsers(t *testing.T, db *gorm.DB, tenantID, modelRef, tenantModelID string, modelType int, users ...string) {
	t.Helper()
	sticky := reconciler.NewStickyGuard()
	for _, u := range users {
		id := tenantModelID
		db.Create(&rentity.UserTenant{ID: "ut-" + u, UserID: u, TenantID: tenantID, Role: "normal", LLMID: modelRef, TenantLLMID: &id})
		if _, err := sticky.RecordObservedUserChoice(context.Background(), db, tenantID, u, modelType, modelRef, id); err != nil {
			t.Fatalf("failed to pin user %s: %v", u, err)
		}
	}
}

func TestRetire_MovesUsersWhoPinnedTheModelToThePlatformDefault(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	chat := int(rentity.ModelTypeChat)
	seedTenant(t, db, "t1", entity.StatusReady)
	db.Create(&entity.PlatformManagedModel{ID: "m-gpt", ModelName: "gpt-4o-mini", ModelType: chat, IsDefault: true, Status: entity.ManagedModelStatusActive})
	w := newTestWorker(db, gw)
	model := publishAndConverge(t, db, gw, w)
	pinned := tenantRow(t, db, "t1", "deepseek-v3")
	pinUsers(t, db, "t1", deepseekRef, pinned.ID, chat, "pinned")
	db.Create(&rentity.UserTenant{ID: "ut-stays", UserID: "stays", TenantID: "t1", Role: "normal", LLMID: gptRef})

	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	w.RunOnce(context.Background())

	var moved, stays rentity.UserTenant
	db.Where("user_id = ?", "pinned").First(&moved)
	db.Where("user_id = ?", "stays").First(&stays)
	if moved.LLMID != gptRef || moved.TenantLLMID == nil || *moved.TenantLLMID != "tm-t1" {
		t.Errorf("a user who pinned the tombstoned model must follow the platform default, got %+v", moved)
	}
	var pref entity.UserDefaultPreference
	db.Where("user_id = ?", "pinned").First(&pref)
	if pref.Origin != entity.OriginPlatformDefault {
		t.Errorf("a moved user must follow the platform default from now on, got %s", pref.Origin)
	}
	if stays.LLMID != gptRef {
		t.Errorf("a user who never used the retired model must be left alone, got %q", stays.LLMID)
	}
}

func TestRetire_ClearsThePinnedDefaultWhenThePlatformHasNone(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	chat := int(rentity.ModelTypeChat)
	seedTenant(t, db, "t1", entity.StatusReady)
	w := newTestWorker(db, gw)
	model := publishAndConverge(t, db, gw, w)
	pinUsers(t, db, "t1", deepseekRef, tenantRow(t, db, "t1", "deepseek-v3").ID, chat, "pinned")

	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	w.RunOnce(context.Background())

	var ut rentity.UserTenant
	db.Where("user_id = ?", "pinned").First(&ut)
	if ut.LLMID != "" || ut.TenantLLMID != nil {
		t.Errorf("without a platform default the retired default must be cleared, got %+v", ut)
	}
}

func TestRetire_KeepsDefaultsOfADeprecatedEmbeddingUntilItIsTombstoned(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("bge-m3")
	embedding := int(rentity.ModelTypeEmbedding)
	seedTenant(t, db, "used", entity.StatusReady)
	w := newTestWorker(db, gw)
	if _, err := New(gw).Publish(context.Background(), db, Request{ModelName: "bge-m3", ModelType: embedding}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	w.RunOnce(context.Background())
	const bgeRef = "bge-m3@platform@OpenAI-API-Compatible"
	bgeID := tenantRow(t, db, "used", "bge-m3").ID
	db.Create(&rentity.UserTenant{ID: "ut-pinned", UserID: "pinned", TenantID: "used", Role: "normal", EmbdID: bgeRef, TenantEmbdID: &bgeID})
	db.Create(&rentity.Knowledgebase{ID: "kb1", TenantID: "used", Name: "kb1", EmbdID: bgeRef})
	var model entity.PlatformManagedModel
	db.Where("model_name = ?", "bge-m3").First(&model)

	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	w.RunOnce(context.Background())

	var ut rentity.UserTenant
	db.Where("user_id = ?", "pinned").First(&ut)
	if ut.EmbdID != bgeRef {
		t.Fatalf("a deprecated embedding model is still readable, so the default must stay, got %q", ut.EmbdID)
	}

	db.Model(&rentity.Knowledgebase{}).Where("id = ?", "kb1").Update("status", "0")
	w.RunOnce(context.Background())

	db.Where("user_id = ?", "pinned").First(&ut)
	if ut.EmbdID != "" {
		t.Errorf("once the model is tombstoned the default must be replaced, got %q", ut.EmbdID)
	}
}
