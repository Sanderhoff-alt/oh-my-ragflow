package publisher

import (
	"context"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"
)

func TestListCatalog_DerivedTenantStatesAndReferences(t *testing.T) {
	db := setupPublishDB(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		seedTenant(t, db, id, entity.StatusReady)
	}
	embedding := int(rentity.ModelTypeEmbedding)
	chat := int(rentity.ModelTypeChat)
	decision := int(rentity.ModelTypeDecision)
	tts := int(rentity.ModelTypeTTS)
	db.Create(&entity.PlatformManagedModel{ID: "m-emb", ModelName: "bge-m3", ModelType: embedding, Status: entity.ManagedModelStatusRetired})
	db.Create(&entity.PlatformManagedModel{ID: "m-chat", ModelName: "deepseek-v3", ModelType: chat, IsDefault: true, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m-dec", ModelName: "qwen-router", ModelType: decision, IsDefault: true, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m-tts", ModelName: "tts-1", ModelType: tts, Status: entity.ManagedModelStatusActive})

	add := func(tenant, name string, modelType int, status, extra string) {
		db.Create(&rentity.TenantModel{ID: "tm-" + tenant + name, ModelName: name, ProviderID: "prov-" + tenant, InstanceID: "inst-" + tenant, ModelType: modelType, Status: status, Extra: extra})
	}
	add("a", "bge-m3", embedding, "active", `{"is_managed":true}`)
	add("b", "bge-m3", embedding, "active", `{"deprecated":true,"is_managed":true,"read_only":true}`)
	add("c", "bge-m3", embedding, "disabled", `{"deprecated":true,"is_managed":true,"tombstoned":true}`)
	add("a", "deepseek-v3", chat, "active", `{"is_managed":true}`)
	add("a", "qwen-router", decision, "active", `{"is_managed":true}`)
	add("a", "tts-1", tts, "active", `{"is_managed":true}`)

	db.Create(&rentity.Knowledgebase{ID: "kb1", TenantID: "b", Name: "kb1", EmbdID: "bge-m3@platform@OpenAI-API-Compatible"})
	db.Create(&rentity.Knowledgebase{ID: "kb2", TenantID: "a", Name: "kb2", EmbdID: "someone-elses-model"})
	decRef := "qwen-router@platform@OpenAI-API-Compatible"
	db.Create(&rentity.Chat{ID: "d1", TenantID: "a", LLMID: "deepseek-v3", DecisionID: &decRef, LLMSetting: rentity.JSONMap{}, PromptConfig: rentity.JSONMap{}, KBIDs: rentity.JSONSlice{}})

	entries, err := ListCatalog(context.Background(), db)
	if err != nil {
		t.Fatalf("ListCatalog failed: %v", err)
	}
	byName := map[string]CatalogEntry{}
	for _, e := range entries {
		byName[e.ModelName] = e
	}

	emb := byName["bge-m3"]
	if emb.Tenants != (TenantStateCounts{Active: 1, Deprecated: 1, Tombstoned: 1}) {
		t.Errorf("unexpected tenant states for the embedding model: %+v", emb.Tenants)
	}
	if emb.Status != entity.ManagedModelStatusRetired || emb.ActiveKBCount != 1 {
		t.Errorf("expected a retired model with one referencing knowledge base, got %+v", emb)
	}
	cm := byName["deepseek-v3"]
	if cm.Tenants.Active != 1 || !cm.IsDefault || cm.ActiveDialogCount != 1 || cm.ActiveKBCount != 0 {
		t.Errorf("unexpected chat model entry: %+v", cm)
	}
	dm := byName["qwen-router"]
	if dm.Tenants.Active != 1 || !dm.IsDefault || dm.ActiveDialogCount != 1 || dm.ActiveKBCount != 0 {
		t.Errorf("unexpected decision model entry: %+v", dm)
	}
	tm := byName["tts-1"]
	if tm.Tenants.Active != 1 || tm.ActiveDialogCount != 0 || tm.ActiveKBCount != 0 {
		t.Errorf("unexpected tts model entry: %+v", tm)
	}
	if len(entries) != 4 || entries[0].ModelName != "bge-m3" {
		t.Errorf("expected entries sorted by name, got %+v", entries)
	}
}

func TestWorker_SweepCreatesTaskForCatalogModelWithoutOne(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "t1", entity.StatusReady)
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat), Status: entity.ManagedModelStatusActive})

	newTestWorker(db, gw).RunOnce(context.Background())

	if _, ok := hasModel(t, db, "t1", "deepseek-v3"); !ok {
		t.Fatal("expected the sweep to roll out a catalog model that has no publish task yet")
	}
	var tasks int64
	db.Model(&entity.ModelPublishTask{}).Where("model_name = ?", "deepseek-v3").Count(&tasks)
	if tasks != 1 {
		t.Errorf("expected the task to be created on demand, got %d", tasks)
	}
}
