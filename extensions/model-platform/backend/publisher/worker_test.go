package publisher

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"

	"gorm.io/gorm"
)

func newTestWorker(db *gorm.DB, gw *fakeGateway, opts ...WorkerOption) *Worker {
	opts = append([]WorkerOption{WithBackoffPolicy(reconciler.FastTestBackoffPolicy)}, opts...)
	return NewWorker(func() *gorm.DB { return db }, gw, opts...)
}

func publishDeepseek(t *testing.T, db *gorm.DB, gw *fakeGateway) *Result {
	t.Helper()
	res, err := New(gw).Publish(context.Background(), db, deepseek)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	return res
}

func itemOf(t *testing.T, db *gorm.DB, taskID, tenantID string) entity.ModelPublishItem {
	t.Helper()
	var item entity.ModelPublishItem
	if err := db.Where("task_id = ? AND tenant_id = ?", taskID, tenantID).First(&item).Error; err != nil {
		t.Fatalf("item %s/%s not found: %v", taskID, tenantID, err)
	}
	return item
}

func TestWorker_AppliesModelAndKeepsExistingWhitelist(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "t1", entity.StatusReady)
	res := publishDeepseek(t, db, gw)

	newTestWorker(db, gw).RunOnce(context.Background())

	tm, ok := hasModel(t, db, "t1", "deepseek-v3")
	if !ok || tm.Status != "active" || tm.Extra != `{"is_managed":true}` {
		t.Fatalf("expected an active managed tenant_model, got %+v (found=%v)", tm, ok)
	}
	got := append([]string(nil), gw.updates["sk-t1"]...)
	sort.Strings(got)
	want := []string{"deepseek-v3", "gpt-4o-mini", "platform/chat"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("whitelist must keep the existing model: want %v, got %v", want, got)
	}

	p, err := GetProgress(context.Background(), db, res.TaskID)
	if err != nil || p.Status != TaskStatusCompleted || p.Succeeded != 1 || p.Total != 1 {
		t.Errorf("unexpected progress %+v (%v)", p, err)
	}
}

func TestWorker_PartialFailureConvergesAfterRetry(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "ok", entity.StatusReady)
	seedTenant(t, db, "flaky", entity.StatusReady)
	gw.setFail("sk-flaky", errors.New("gateway timeout"))
	res := publishDeepseek(t, db, gw)
	w := newTestWorker(db, gw)

	w.RunOnce(context.Background())

	if item := itemOf(t, db, res.TaskID, "ok"); item.Status != entity.PublishItemSucceeded {
		t.Errorf("healthy tenant must not be blocked by a failing one, got %s", item.Status)
	}
	flaky := itemOf(t, db, res.TaskID, "flaky")
	if flaky.Status != entity.PublishItemPending || flaky.RetryCount != 1 || flaky.NextRetryAt == nil || flaky.LastError == "" {
		t.Errorf("failing tenant must be pending with a backoff and an error, got %+v", flaky)
	}
	if _, ok := hasModel(t, db, "flaky", "deepseek-v3"); ok {
		t.Error("tenant_model must not exist before the gateway accepted the whitelist")
	}
	if p, _ := GetProgress(context.Background(), db, res.TaskID); p.Status != TaskStatusRunning || p.Succeeded != 1 || p.Pending != 1 {
		t.Errorf("unexpected progress %+v", p)
	}

	gw.setFail("sk-flaky", nil)
	time.Sleep(60 * time.Millisecond)
	w.RunOnce(context.Background())

	if item := itemOf(t, db, res.TaskID, "flaky"); item.Status != entity.PublishItemSucceeded {
		t.Errorf("expected the retry to converge, got %+v", item)
	}
	if _, ok := hasModel(t, db, "flaky", "deepseek-v3"); !ok {
		t.Error("expected tenant_model after the retry")
	}
}

func TestWorker_FailsAfterMaxRetriesAndManualRetryRecovers(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "t1", entity.StatusReady)
	gw.setFail("sk-t1", errors.New("gateway down"))
	res := publishDeepseek(t, db, gw)
	w := newTestWorker(db, gw, WithMaxRetries(2))

	w.RunOnce(context.Background())
	time.Sleep(60 * time.Millisecond)
	w.RunOnce(context.Background())

	if item := itemOf(t, db, res.TaskID, "t1"); item.Status != entity.PublishItemFailed || item.NextRetryAt != nil {
		t.Fatalf("expected a terminal failed item, got %+v", item)
	}
	failed, total, err := ListFailedItems(context.Background(), db, res.TaskID, 1, 20)
	if err != nil || total != 1 || failed[0].TenantID != "t1" || failed[0].LastError == "" {
		t.Fatalf("unexpected failed items %+v total=%d err=%v", failed, total, err)
	}
	if p, _ := GetProgress(context.Background(), db, res.TaskID); p.Status != TaskStatusPartialFailed || p.Failed != 1 {
		t.Errorf("unexpected progress %+v", p)
	}

	gw.setFail("sk-t1", nil)
	if n, err := RetryFailed(context.Background(), db, res.TaskID); err != nil || n != 1 {
		t.Fatalf("RetryFailed reset %d items (%v)", n, err)
	}
	w.RunOnce(context.Background())

	if item := itemOf(t, db, res.TaskID, "t1"); item.Status != entity.PublishItemSucceeded {
		t.Errorf("expected recovery after the manual retry, got %+v", item)
	}
}

func TestWorker_SweepCoversTenantsReadyAfterPublish(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "early", entity.StatusReady)
	seedTenant(t, db, "late", entity.StatusPending)
	res := publishDeepseek(t, db, gw)
	w := newTestWorker(db, gw)
	w.RunOnce(context.Background())

	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", "late").Update("status", entity.StatusReady)
	w.RunOnce(context.Background())

	if _, ok := hasModel(t, db, "late", "deepseek-v3"); !ok {
		t.Fatal("expected the sweep to roll the model out to a tenant that became Ready later")
	}
	if p, _ := GetProgress(context.Background(), db, res.TaskID); p.Total != 2 || p.Succeeded != 2 {
		t.Errorf("expected the swept tenant in the latest task, got %+v", p)
	}
}

func TestWorker_RecoversStaleLease(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "t1", entity.StatusReady)
	res := publishDeepseek(t, db, gw)
	db.Model(&entity.ModelPublishItem{}).Where("task_id = ?", res.TaskID).
		Updates(map[string]interface{}{"status": entity.PublishItemRunning, "updated_at": time.Now().Add(-time.Hour)})

	newTestWorker(db, gw).RunOnce(context.Background())

	if item := itemOf(t, db, res.TaskID, "t1"); item.Status != entity.PublishItemSucceeded {
		t.Errorf("expected the abandoned item to be recovered and applied, got %+v", item)
	}
}

func TestWorker_ConcurrentWorkersApplyEachTenantOnce(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	for _, id := range []string{"a", "b", "c", "d"} {
		seedTenant(t, db, id, entity.StatusReady)
	}
	publishDeepseek(t, db, gw)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			newTestWorker(db, gw).RunOnce(context.Background())
		}()
	}
	wg.Wait()

	if gw.calls() != 4 {
		t.Errorf("expected one gateway update per tenant, got %d", gw.calls())
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		var n int64
		db.Table("tenant_model").Where("instance_id = ? AND model_name = ?", "inst-"+id, "deepseek-v3").Count(&n)
		if n != 1 {
			t.Errorf("tenant %s has %d deepseek-v3 rows, want 1", id, n)
		}
	}
}

func TestPublish_RepeatedPublishIsIdempotent(t *testing.T) {
	db := setupPublishDB(t)
	gw := newFakeGateway("deepseek-v3")
	seedTenant(t, db, "t1", entity.StatusReady)
	publishDeepseek(t, db, gw)
	newTestWorker(db, gw).RunOnce(context.Background())
	before := gw.calls()

	again := publishDeepseek(t, db, gw)
	newTestWorker(db, gw).RunOnce(context.Background())

	if again.Total != 0 {
		t.Errorf("expected no new items once every tenant has the model, got %d", again.Total)
	}
	if gw.calls() != before {
		t.Errorf("an idempotent publish must not call the gateway again (%d -> %d)", before, gw.calls())
	}
	var catalog int64
	db.Model(&entity.PlatformManagedModel{}).Count(&catalog)
	if catalog != 1 {
		t.Errorf("expected a single catalog row, got %d", catalog)
	}
}

func TestGetProgress_UnknownTask(t *testing.T) {
	db := setupPublishDB(t)
	if _, err := GetProgress(context.Background(), db, "missing"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound, got %v", err)
	}
}
