package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func createTestTenantInDB(db *gorm.DB, tenantID string) {
	db.Exec("INSERT INTO tenant (id, status, parser_ids) VALUES (?, '1', '1')", tenantID)
	db.Exec("INSERT INTO user_tenant (id, user_id, tenant_id, role) VALUES (?, ?, ?, 'owner')", "ut-"+tenantID, "user-"+tenantID, tenantID)
}

func TestSelfHealing_Tier1ImmediateSuccess(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, bus, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(10*time.Second),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	tenantID := "tenant-tier1-success"
	createTestTenantInDB(db, tenantID)

	// Emit TenantCreated event
	bus.Publish(event.NewTenantCreatedEvent(tenantID))

	// Wait for Tier 1 success
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier1SuccessCount() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if ctrl.Tier1SuccessCount() != 1 {
		t.Fatalf("expected 1 Tier 1 success, got %d", ctrl.Tier1SuccessCount())
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to get state: %v", err)
	}
	if state.Status != entity.StatusReady {
		t.Errorf("expected ready, got %s", state.Status)
	}
}

func TestSelfHealing_Tier1SingleAttemptHandsOffToTier2(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	// Gateway is down: returns connection refused
	mockClient.generateErr = errors.New("gateway connection refused (503)")

	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, bus, rec,
		WithBackoffPolicy(DefaultBackoffPolicy),
		WithPollerInterval(10*time.Second),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	tenantID := "tenant-tier1-single-attempt"
	createTestTenantInDB(db, tenantID)

	// Emit event
	bus.Publish(event.NewTenantCreatedEvent(tenantID))

	// Wait for single Tier 1 attempt to fail and hand off
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier1FailureCount() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if ctrl.Tier1FailureCount() != 1 {
		t.Fatalf("expected Tier 1 failure count 1, got %d", ctrl.Tier1FailureCount())
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status degraded after tier 1 failure, got %s", state.Status)
	}
	if state.RetryCount != 1 {
		t.Errorf("expected retry_count to be 1 after single Tier-1 attempt, got %d", state.RetryCount)
	}
	if state.NextRetryAt == nil {
		t.Fatal("expected next_retry_at to be populated for Tier 2 healing")
	}
	delay := time.Until(*state.NextRetryAt)
	if delay < 3*time.Second || delay > 7*time.Second {
		t.Errorf("expected first retry delay around DefaultBackoffPolicy.Intervals[0] (~5s), got %v", delay)
	}
}

func TestSelfHealing_OutageAndTier2SelfHealing(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	// 1. Gateway initially failing (simulating outage window)
	mockClient.generateErr = errors.New("gateway unavailable: 502 Bad Gateway")

	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, bus, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(50*time.Millisecond),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	tenantID := "tenant-outage-healing"
	createTestTenantInDB(db, tenantID)

	// Emit event
	bus.Publish(event.NewTenantCreatedEvent(tenantID))

	// Wait for Tier 1 single attempt to fail and enter Degraded
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier1FailureCount() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ctrl.Tier1FailureCount() != 1 {
		t.Fatalf("expected tier 1 failure count 1, got %d", ctrl.Tier1FailureCount())
	}

	// 2. Outage resolved! Gateway comes back online!
	mockClient.mu.Lock()
	mockClient.generateErr = nil
	mockClient.mu.Unlock()

	// Update next_retry_at to now to simulate time passing past the outage window
	db.Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ?", tenantID).
		Update("next_retry_at", time.Now().Add(-1*time.Second))

	// Trigger Tier 2 polling cycle
	ctrl.PollAndHeal(ctx)

	// Wait for Tier 2 healing
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier2HealedCount() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if ctrl.Tier2HealedCount() < 1 {
		t.Fatalf("expected at least 1 Tier 2 healed record, got %d", ctrl.Tier2HealedCount())
	}

	// Verify tenant is now healthy and Ready!
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusReady {
		t.Errorf("expected tenant to be healed to status ready, got %s", state.Status)
	}
	if state.RetryCount != 0 {
		t.Errorf("expected retry_count to reset to 0, got %d", state.RetryCount)
	}
}

func TestSelfHealing_MaxRetriesExceeded(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithMaxRetries(5),
	)

	tenantID := "tenant-exceeded-retries"
	createTestTenantInDB(db, tenantID)

	past := time.Now().Add(-10 * time.Minute)
	// Seed record that already reached 5 retries
	exhausted := entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  5,
		MaxRetries:  5,
		NextRetryAt: &past,
		Version:     1,
	}
	db.Create(&exhausted)

	// Run Poller
	ctrl.PollAndHeal(ctx)

	if ctrl.FailedCount() != 1 {
		t.Errorf("expected failedCount 1, got %d", ctrl.FailedCount())
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusFailed {
		t.Errorf("expected status failed, got %s", state.Status)
	}
}

func TestSelfHealing_MultiReplicaConcurrency(t *testing.T) {
	db := setupTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	// Start Pod Replica 1
	ctrl1 := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)
	ctrl1.Start(ctx)
	defer ctrl1.Stop()

	// Start Pod Replica 2
	ctrl2 := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)
	ctrl2.Start(ctx)
	defer ctrl2.Stop()

	// Seed 10 degraded tenants due for retry
	const tenantCount = 10
	past := time.Now().Add(-10 * time.Second)
	for i := 0; i < tenantCount; i++ {
		tID := fmt.Sprintf("tenant-replica-%d", i)
		createTestTenantInDB(db, tID)
		recRecord := entity.TenantReconcileRecord{
			TenantID:    tID,
			Status:      entity.StatusDegraded,
			RetryCount:  1,
			MaxRetries:  50,
			NextRetryAt: &past,
			Version:     1,
		}
		db.Create(&recRecord)
	}

	// Trigger polling on both replicas concurrently
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			ctrl1.PollAndHeal(ctx)
		}()
		go func() {
			defer wg.Done()
			ctrl2.PollAndHeal(ctx)
		}()
	}
	wg.Wait()

	// Wait up to 3 seconds for all 10 tenants to heal to Ready
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var readyCount int64
		db.Model(&entity.TenantReconcileRecord{}).Where("status = ?", entity.StatusReady).Count(&readyCount)
		if readyCount == tenantCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	var readyCount int64
	db.Model(&entity.TenantReconcileRecord{}).Where("status = ?", entity.StatusReady).Count(&readyCount)
	if readyCount != tenantCount {
		t.Fatalf("expected all %d tenants to heal to ready, got %d", tenantCount, readyCount)
	}

	// Verify no duplicate instances were created in DB
	for i := 0; i < tenantCount; i++ {
		tID := fmt.Sprintf("tenant-replica-%d", i)
		var instCount int64
		db.Model(&rentity.TenantModelInstance{}).
			Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model_instance.provider_id").
			Where("tenant_model_provider.tenant_id = ?", tID).
			Count(&instCount)

		if instCount != 1 {
			t.Errorf("tenant %s has %d instances, expected 1", tID, instCount)
		}
	}
}

func TestSelfHealing_DisablePoller_RunsTier1Only(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	// Simulate Admin server: Tier-1 enabled, Tier-2 poller disabled
	ctrl := NewSelfHealingController(
		db, bus, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(20*time.Millisecond),
		WithDisablePoller(),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	// Pre-seed a pending record that did NOT emit an event on this bus:
	// with poller disabled, it must remain pending and never be polled.
	unpolledID := "tenant-unpolled-pending"
	createTestTenantInDB(db, unpolledID)
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   unpolledID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	})

	// Emit event for a freshly created tenant: Tier-1 must provision it immediately.
	eventID := "tenant-admin-tier1"
	createTestTenantInDB(db, eventID)
	bus.Publish(event.NewTenantCreatedEvent(eventID))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier1SuccessCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ctrl.Tier1SuccessCount() != 1 {
		t.Fatalf("expected Tier1SuccessCount=1 in Tier-1-only mode, got %d", ctrl.Tier1SuccessCount())
	}

	// Wait longer than several pollerInterval ticks to prove no background poller ran.
	time.Sleep(120 * time.Millisecond)

	rdao := dao.NewReconcileDAO()
	eventState, err := rdao.GetByTenantID(ctx, db, eventID)
	if err != nil || eventState.Status != entity.StatusReady {
		t.Fatalf("expected event tenant ready, got state=%+v err=%v", eventState, err)
	}

	unpolledState, err := rdao.GetByTenantID(ctx, db, unpolledID)
	if err != nil || unpolledState.Status != entity.StatusPending {
		t.Fatalf("expected unpolled tenant to stay pending when poller is disabled, got state=%+v err=%v", unpolledState, err)
	}
	if ctrl.Tier2HealedCount() != 0 {
		t.Fatalf("expected Tier2HealedCount=0 when poller is disabled, got %d", ctrl.Tier2HealedCount())
	}
}

func TestSelfHealing_AdminTier1AndAPITier2ConcurrentRaceProducesOneKey(t *testing.T) {
	db := setupTestDB(t)
	adminBus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adminBus.Start(ctx)
	defer adminBus.Stop()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	// Admin controller: Tier-1 only
	adminCtrl := NewSelfHealingController(
		db, adminBus, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithDisablePoller(),
	)
	adminCtrl.Start(ctx)
	defer adminCtrl.Stop()

	// API controller: Tier-2 poller (no local bus event from admin process)
	apiCtrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(10*time.Second),
	)
	apiCtrl.Start(ctx)
	defer apiCtrl.Stop()

	const tenantID = "tenant-cross-proc-race"
	createTestTenantInDB(db, tenantID)
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	})

	// Barrier before gorm:begin_transaction so both Admin Tier-1 and API Tier-2
	// have already read the exact same claimable pending version (v=1) without
	// holding the single SQLite pool connection, and then race on the claim UPDATE.
	const callbackName = "test:sync_admin_api_claim"
	var arrived atomic.Int32
	bothArrived := make(chan struct{})
	release := make(chan struct{})
	var closeArrivedOnce sync.Once
	var closeReleaseOnce sync.Once
	releaseAll := func() { closeReleaseOnce.Do(func() { close(release) }) }
	if err := db.Callback().Update().Before("gorm:begin_transaction").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil {
			return
		}
		table := tx.Statement.Table
		if table == "" && tx.Statement.Schema != nil {
			table = tx.Statement.Schema.Table
		}
		if table != "platform_tenant_reconcile" {
			return
		}
		n := arrived.Add(1)
		if n <= 2 {
			if n == 2 {
				closeArrivedOnce.Do(func() { close(bothArrived) })
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
	}); err != nil {
		t.Fatalf("failed to register claim barrier callback: %v", err)
	}
	defer func() {
		releaseAll()
		adminCtrl.Stop()
		apiCtrl.Stop()
		_ = db.Callback().Update().Remove(callbackName)
	}()

	adminBus.Publish(event.NewTenantCreatedEvent(tenantID))
	var pollWG sync.WaitGroup
	pollWG.Add(1)
	go func() {
		defer pollWG.Done()
		apiCtrl.PollAndHeal(ctx)
	}()

	select {
	case <-bothArrived:
		releaseAll()
	case <-time.After(3 * time.Second):
		releaseAll()
		pollWG.Wait()
		t.Fatalf("timed out waiting for both Admin Tier-1 and API Tier-2 to reach the claim barrier (arrived=%d)", arrived.Load())
	}
	pollWG.Wait()
	// Drain the Admin Tier-1 handler goroutine so both callers have fully completed before asserting.
	adminCtrl.Stop()

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch tenant state: %v", err)
	}
	if state.Status != entity.StatusReady {
		t.Fatalf("expected tenant ready, got %s", state.Status)
	}

	// Optimistic locking ensures exactly one caller won the claim and issued one key.
	if got := atomic.LoadInt32(&mockClient.keyCounter); got != 1 {
		t.Fatalf("expected exactly 1 gateway key issued across competing Admin Tier-1 and API Tier-2 claims, got %d", got)
	}
	if totalSuccess := adminCtrl.Tier1SuccessCount() + apiCtrl.Tier2HealedCount(); totalSuccess < 1 {
		t.Fatalf("expected at least 1 controller to record completion, got admin=%d api=%d", adminCtrl.Tier1SuccessCount(), apiCtrl.Tier2HealedCount())
	}
}

func TestSelfHealing_Tier1RespectsFutureDegradedDeadline(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(DefaultBackoffPolicy),
		WithDisablePoller(),
	)

	tenantID := "tenant-tier1-future-deadline"
	createTestTenantInDB(db, tenantID)

	future := time.Now().Add(5 * time.Minute)
	// Simulate API Tier-2 already having attempted and backed off the tenant before a delayed Admin event runs.
	db.Create(&entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  1,
		MaxRetries:  50,
		NextRetryAt: &future,
		Version:     2,
	})

	ctrl.handleTier1Event(ctx, tenantID)

	if calls := atomic.LoadInt32(&mockClient.keyCounter); calls != 0 {
		t.Fatalf("expected delayed Tier-1 event to respect future next_retry_at (0 gateway calls), got %d", calls)
	}
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded || state.Version != 2 {
		t.Fatalf("expected degraded v2 untouched, got status=%s version=%d", state.Status, state.Version)
	}
}

func TestBackoffPolicy_ProductionScheduleAndCap(t *testing.T) {
	want := []time.Duration{
		5 * time.Second,
		15 * time.Second,
		30 * time.Second,
		1 * time.Minute,
		2 * time.Minute,
	}
	if len(DefaultBackoffPolicy.Intervals) != len(want) {
		t.Fatalf("expected %d intervals, got %d", len(want), len(DefaultBackoffPolicy.Intervals))
	}
	for i, w := range want {
		if DefaultBackoffPolicy.Intervals[i] != w {
			t.Errorf("interval[%d]: expected %v, got %v", i, w, DefaultBackoffPolicy.Intervals[i])
		}
	}

	// Verify jitter stays within [base, min(base + base/10, 2m)] per slot, with the final slot capped at 2m.
	for attempt := 0; attempt < 10; attempt++ {
		expectedBase := want[len(want)-1]
		if attempt < len(want) {
			expectedBase = want[attempt]
		}
		maxDelay := expectedBase + expectedBase/10
		if maxDelay > 2*time.Minute {
			maxDelay = 2 * time.Minute
		}
		for i := 0; i < 50; i++ {
			delay := DefaultBackoffPolicy.CalculateBackoff(attempt)
			if delay < expectedBase || delay > maxDelay {
				t.Fatalf("attempt %d: expected delay in [%v, %v], got %v", attempt, expectedBase, maxDelay, delay)
			}
		}
	}
}

func TestTenantReconciler_MaxRetriesExceeded_DirectReconcileMarksFailed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	tenantID := "tenant-direct-max-retries"
	createTestTenantInDB(db, tenantID)

	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusDegraded,
		RetryCount: 50,
		MaxRetries: 50,
		Version:    1,
	})

	err := rec.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("expected nil from Reconcile when marking failed on max retries, got %v", err)
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusFailed {
		t.Errorf("expected status failed, got %s", state.Status)
	}
}

func TestPollAndHeal_SkipsRecordWhenNextRetryAtIsInFuture(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-future-deadline"
	createTestTenantInDB(db, tenantID)

	future := time.Now().Add(5 * time.Minute)
	past := time.Now().Add(-1 * time.Minute)

	// Record was originally due (past) when polled, but another replica rescheduled it to future
	db.Create(&entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  1,
		MaxRetries:  50,
		NextRetryAt: &past,
		Version:     1,
	})

	// Simulate concurrent replica rescheduling it right after FindDueRetries
	db.Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ?", tenantID).
		Updates(map[string]interface{}{
			"next_retry_at": future,
			"version":       2,
		})

	ctrl.PollAndHeal(ctx)

	// Verify gateway was never called because next_retry_at is in the future
	if calls := atomic.LoadInt32(&mockClient.keyCounter); calls != 0 {
		t.Fatalf("expected 0 gateway calls, got %d", calls)
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status to remain degraded, got %s", state.Status)
	}
}

func TestReconcile_WithEnforceDeadline_RejectsRescheduledFutureRecord(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	tenantID := "tenant-rescheduled-race"
	createTestTenantInDB(db, tenantID)

	future := time.Now().Add(10 * time.Minute)
	// Seed record where another replica recently rescheduled it to the future
	db.Create(&entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  2,
		MaxRetries:  50,
		NextRetryAt: &future,
		Version:     3,
	})

	// Reconcile with deadline enforcement (used by Tier-2 poller) must abort without making gateway calls
	err := rec.Reconcile(ctx, db, tenantID, WithEnforceDeadline())
	if err != nil {
		t.Fatalf("expected nil from Reconcile when deadline in future, got %v", err)
	}

	if calls := atomic.LoadInt32(&mockClient.keyCounter); calls != 0 {
		t.Fatalf("expected 0 gateway calls, got %d", calls)
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status to remain degraded, got %s", state.Status)
	}
}

func TestReconcile_WithoutEnforceDeadline_AllowsImmediateReconcile(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	tenantID := "tenant-tier1-unrestricted"
	createTestTenantInDB(db, tenantID)

	future := time.Now().Add(10 * time.Minute)
	// Seed record with future deadline (e.g. from an earlier degraded schedule)
	db.Create(&entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  1,
		MaxRetries:  50,
		NextRetryAt: &future,
		Version:     1,
	})

	// Default Reconcile (without WithEnforceDeadline) bypasses next_retry_at and must succeed
	err := rec.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("expected nil from Reconcile, got %v", err)
	}

	if calls := atomic.LoadInt32(&mockClient.keyCounter); calls != 1 {
		t.Fatalf("expected 1 gateway call, got %d", calls)
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusReady {
		t.Errorf("expected status to be ready, got %s", state.Status)
	}
}

func TestSelfHealing_Tier1NoOpStatusProvisioning_DoesNotIncrementSuccessCount(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-tier1-noop"
	createTestTenantInDB(db, tenantID)

	// Pre-create record in provisioning status so Reconcile returns nil as a no-op
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusProvisioning,
		RetryCount: 1,
		MaxRetries: 50,
		Version:    1,
	})

	ctrl.handleTier1Event(ctx, tenantID)

	if count := ctrl.Tier1SuccessCount(); count != 0 {
		t.Fatalf("expected Tier1SuccessCount to be 0 for provisioning no-op, got %d", count)
	}
}

func TestPollAndHeal_UnifiedBackoffAdvancesByDBRetryCount(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	mockClient.generateErr = errors.New("gateway unavailable")
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(DefaultBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-unified-backoff"
	createTestTenantInDB(db, tenantID)

	// Record enters as 'pending' with retry_count = 0
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	})

	// Verify 7 consecutive failures advance strictly by DB retry_count across
	// 5s -> 15s -> 30s -> 1m -> 2m (capped at 2m) with no in-memory attempt map.
	expectedBases := []time.Duration{
		5 * time.Second,  // retry 1 -> Intervals[0]
		15 * time.Second, // retry 2 -> Intervals[1]
		30 * time.Second, // retry 3 -> Intervals[2]
		1 * time.Minute,  // retry 4 -> Intervals[3]
		2 * time.Minute,  // retry 5 -> Intervals[4] (cap)
		2 * time.Minute,  // retry 6 -> cap
		2 * time.Minute,  // retry 7 -> cap
	}

	rdao := dao.NewReconcileDAO()
	for i, base := range expectedBases {
		if i > 0 {
			db.Model(&entity.TenantReconcileRecord{}).
				Where("tenant_id = ?", tenantID).
				Update("next_retry_at", time.Now().Add(-1*time.Second))
		}

		// Use a fresh controller instance on odd iterations to prove backoff
		// progression depends solely on DB retry_count (survives restarts / multi-replica).
		activeCtrl := ctrl
		if i%2 == 1 {
			activeCtrl = NewSelfHealingController(
				db, nil, rec,
				WithBackoffPolicy(DefaultBackoffPolicy),
			)
		}
		before := time.Now()
		activeCtrl.PollAndHeal(ctx)
		after := time.Now()

		state, err := rdao.GetByTenantID(ctx, db, tenantID)
		if err != nil {
			t.Fatalf("attempt %d: failed to fetch state: %v", i+1, err)
		}
		if state.Status != entity.StatusDegraded {
			t.Fatalf("attempt %d: expected status degraded, got %s", i+1, state.Status)
		}
		if state.RetryCount != i+1 {
			t.Fatalf("attempt %d: expected retry_count=%d, got %d", i+1, i+1, state.RetryCount)
		}
		if state.NextRetryAt == nil {
			t.Fatalf("attempt %d: expected next_retry_at to be set", i+1)
		}
		maxStep := base + base/10
		if maxStep > 2*time.Minute {
			maxStep = 2 * time.Minute
		}
		minDeadline := before.Add(base)
		maxDeadline := after.Add(maxStep)
		if state.NextRetryAt.Before(minDeadline) || state.NextRetryAt.After(maxDeadline) {
			t.Fatalf("attempt %d: expected NextRetryAt in [%v, %v], got %v", i+1, minDeadline, maxDeadline, *state.NextRetryAt)
		}
	}
}

func TestPollAndHeal_RecoversStaleProvisioningRecord(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-stale-provisioning"
	createTestTenantInDB(db, tenantID)

	// Seed record stuck in 'provisioning' from a crashed worker (updated 5 minutes ago)
	staleTime := time.Now().Add(-5 * time.Minute)
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusProvisioning,
		RetryCount: 1,
		MaxRetries: 50,
		Version:    1,
		CreatedAt:  staleTime,
		UpdatedAt:  staleTime,
	})
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Update("updated_at", staleTime)

	// 1st PollAndHeal: discovers stale provisioning record and recovers it to degraded
	ctrl.PollAndHeal(ctx)

	rdao := dao.NewReconcileDAO()
	state1, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state1: %v", err)
	}
	if state1.Status != entity.StatusDegraded {
		t.Fatalf("expected state to recover to degraded, got %s", state1.Status)
	}
	if state1.LastError != "recovered stale provisioning lease" {
		t.Errorf("expected LastError to indicate recovered lease, got %s", state1.LastError)
	}

	// 2nd PollAndHeal: record is now due degraded -> reconciles successfully to Ready
	ctrl.PollAndHeal(ctx)

	state2, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state2: %v", err)
	}
	if state2.Status != entity.StatusReady {
		t.Fatalf("expected state to converge to ready, got %s", state2.Status)
	}
}
