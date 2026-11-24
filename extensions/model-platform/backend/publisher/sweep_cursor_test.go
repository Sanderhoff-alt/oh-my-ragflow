package publisher

import (
	"context"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"
)

func TestSweepDeprecated_ResumesAfterTheLastBatchAndWrapsAround(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("bge-m3")
	embedding := int(rentity.ModelTypeEmbedding)
	for _, id := range []string{"a", "b"} {
		seedTenant(t, db, id, entity.StatusReady)
	}
	w := newTestWorker(db, gw)
	if _, err := New(gw).Publish(context.Background(), db, Request{ModelName: "bge-m3", ModelType: embedding}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	w.RunOnce(context.Background())
	for _, id := range []string{"a", "b"} {
		db.Create(&rentity.Knowledgebase{ID: "kb-" + id, TenantID: id, Name: id, EmbdID: "bge-m3@platform@OpenAI-API-Compatible"})
	}
	var model entity.PlatformManagedModel
	db.Where("model_name = ?", "bge-m3").First(&model)
	if _, err := Retire(context.Background(), db, model.ID); err != nil {
		t.Fatalf("Retire failed: %v", err)
	}
	w.RunOnce(context.Background())
	db.Model(&rentity.Knowledgebase{}).Where("1 = 1").Update("status", "0")

	tombstoned := func(tenantID string) bool {
		_, gone := reconciler.RetirementFlags(tenantRow(t, db, tenantID, "bge-m3").Extra)
		return gone
	}

	w.sweepAfter = "a"
	w.sweepDeprecated(context.Background(), db)
	if tombstoned("a") || !tombstoned("b") {
		t.Errorf("a batch that resumes after tenant a must only handle the tenants behind it")
	}

	w.sweepAfter = "zzz"
	w.sweepDeprecated(context.Background(), db)
	if !tombstoned("a") {
		t.Errorf("a cursor past the last candidate must wrap around to the first tenant")
	}
	if w.sweepAfter != "" {
		t.Errorf("a partial batch must reset the cursor, got %q", w.sweepAfter)
	}
}
