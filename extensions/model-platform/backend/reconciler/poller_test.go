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

func TestSelfHealing_Tier1ExhaustionToDegraded(t *testing.T) {
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
		WithBackoffPolicy(FastTestBackoffPolicy),
		WithPollerInterval(500*time.Millisecond),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	tenantID := "tenant-tier1-exhaust"
	createTestTenantInDB(db, tenantID)

	// Emit event
	bus.Publish(event.NewTenantCreatedEvent(tenantID))

	// Wait for Tier 1 exhaustion
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
		t.Errorf("expected status degraded after tier 1 exhaustion, got %s", state.Status)
	}
	if state.RetryCount != len(FastTestBackoffPolicy.Tier1Intervals) {
		t.Errorf("expected retry_count to be exactly %d after fast retries, got %d", len(FastTestBackoffPolicy.Tier1Intervals), state.RetryCount)
	}
	if state.NextRetryAt == nil {
		t.Error("expected next_retry_at to be populated for Tier 2 healing")
	}
}

func TestSelfHealing_45SecondOutageAndTier2SelfHealing(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	// 1. Gateway initially failing (simulating 45s maintenance window)
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

	// Wait for Tier 1 to exhaust and enter Degraded
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

func TestSelfHealing_Tier1Exhaustion_PreservesActiveProvisioningClaim(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	mockClient.generateErr = errors.New("gateway unavailable")
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctrl := NewSelfHealingController(
		db, bus, rec,
		WithBackoffPolicy(FastTestBackoffPolicy),
	)
	ctrl.Start(ctx)
	defer ctrl.Stop()

	tenantID := "tenant-provisioning-preserve"
	createTestTenantInDB(db, tenantID)

	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	})

	bus.Publish(event.NewTenantCreatedEvent(tenantID))

	// Simulate concurrent worker transitioning to StatusProvisioning before Tier 1 finishes
	time.Sleep(10 * time.Millisecond)
	_ = db.Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ?", tenantID).
		Update("status", entity.StatusProvisioning).Error

	// Wait for Tier 1 to exhaust
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.Tier1FailureCount() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusProvisioning {
		t.Errorf("expected status to remain provisioning, got %s", state.Status)
	}
}

func TestBackoffPolicy_ProductionTier2Schedule(t *testing.T) {
	if len(DefaultBackoffPolicy.Tier2Intervals) == 0 {
		t.Fatal("expected non-empty Tier2Intervals")
	}
	firstInterval := DefaultBackoffPolicy.Tier2Intervals[0]
	if firstInterval != 15*time.Second {
		t.Errorf("expected first Tier 2 interval to be 15s to meet 45s recovery target, got %v", firstInterval)
	}
}

func TestBackoffPolicy_Tier2CapAfterJitter(t *testing.T) {
	for attempt := 4; attempt < 10; attempt++ {
		for i := 0; i < 50; i++ {
			delay := DefaultBackoffPolicy.CalculateTier2Backoff(attempt)
			if delay > 10*time.Minute {
				t.Fatalf("expected delay <= 10m, got %v for attempt %d", delay, attempt)
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

func TestReconcile_WithoutEnforceDeadline_AllowsTier1Retries(t *testing.T) {
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

	// Default Reconcile (without WithEnforceDeadline) is used by Tier-1 and must succeed
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

func TestPollAndHeal_CalculatesTier2ScheduleRelativeTier2Attempt(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	mockClient.generateErr = errors.New("gateway unavailable")
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	policy := BackoffPolicy{
		Tier1Intervals: []time.Duration{10 * time.Millisecond},
		Tier2Intervals: []time.Duration{
			15 * time.Second,
			1 * time.Minute,
			2 * time.Minute,
			5 * time.Minute,
			10 * time.Minute,
		},
	}

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(policy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-tier2-schedule-offset"
	createTestTenantInDB(db, tenantID)

	past := time.Now().Add(-1 * time.Minute)
	// Seed tenant having completed Tier 1 (retry_count = 1 = len(Tier1Intervals))
	db.Create(&entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  1,
		MaxRetries:  50,
		NextRetryAt: &past,
		Version:     1,
	})

	ctrl.PollAndHeal(ctx)

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Fatalf("expected status degraded, got %s", state.Status)
	}
	// retry_count is now 2. maxTier1Retries = 1.
	// tier2Attempt = 2 - 1 = 1.
	// Expected delay should be roughly around Tier2Intervals[1] (1 minute, plus <= 10% jitter),
	// and NOT Tier2Intervals[4] (10 minutes).
	if state.NextRetryAt == nil {
		t.Fatal("expected next_retry_at to be set")
	}
	delay := time.Until(*state.NextRetryAt)
	if delay > 2*time.Minute {
		t.Fatalf("expected delay to use Tier2Intervals[1] (~1m), but got %v", delay)
	}
	if delay < 40*time.Second {
		t.Fatalf("expected delay >= 40s (around 1m), got %v", delay)
	}
}

func TestPollAndHeal_PendingRecordDirectToTier2_AdvancesSchedule(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	mockClient := newMockGatewayClient()
	mockClient.generateErr = errors.New("gateway unavailable")
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	policy := BackoffPolicy{
		Tier1Intervals: []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond},
		Tier2Intervals: []time.Duration{
			15 * time.Second,
			1 * time.Minute,
			2 * time.Minute,
			5 * time.Minute,
			10 * time.Minute,
		},
	}

	ctrl := NewSelfHealingController(
		db, nil, rec,
		WithBackoffPolicy(policy),
		WithPollerInterval(30*time.Millisecond),
	)

	tenantID := "tenant-pending-to-tier2"
	createTestTenantInDB(db, tenantID)

	// Record enters Tier-2 directly as 'pending' with 0 retries
	db.Create(&entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	})

	// 1st attempt in Tier-2: fails -> should schedule Tier2Intervals[0] (~15s)
	ctrl.PollAndHeal(ctx)

	rdao := dao.NewReconcileDAO()
	state1, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state1: %v", err)
	}
	if state1.Status != entity.StatusDegraded {
		t.Fatalf("expected status degraded after first failure, got %s", state1.Status)
	}
	if state1.NextRetryAt == nil {
		t.Fatal("expected next_retry_at to be set on state1")
	}
	delay1 := time.Until(*state1.NextRetryAt)
	if delay1 > 30*time.Second {
		t.Fatalf("expected delay1 <= 30s (~15s), got %v", delay1)
	}

	// Advance clock to simulate due retry for 2nd attempt
	past := time.Now().Add(-1 * time.Minute)
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Updates(map[string]interface{}{
		"next_retry_at": past,
		"version":       state1.Version,
	})

	// 2nd attempt in Tier-2: fails -> should schedule Tier2Intervals[1] (~1m)
	ctrl.PollAndHeal(ctx)

	state2, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state2: %v", err)
	}
	delay2 := time.Until(*state2.NextRetryAt)
	if delay2 < 40*time.Second || delay2 > 2*time.Minute {
		t.Fatalf("expected delay2 to advance to ~1m, got %v", delay2)
	}

	// 3rd through 7th attempts: simulate subsequent failures to verify schedule continues to advance
	// and remains capped at 10m past the 5th failure without resetting.
	lastState := state2
	for attempt := 3; attempt <= 7; attempt++ {
		db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Updates(map[string]interface{}{
			"next_retry_at": time.Now().Add(-1 * time.Minute),
			"version":       lastState.Version,
		})
		ctrl.PollAndHeal(ctx)

		currentState, err := rdao.GetByTenantID(ctx, db, tenantID)
		if err != nil {
			t.Fatalf("failed to fetch state at attempt %d: %v", attempt, err)
		}
		delay := time.Until(*currentState.NextRetryAt)

		switch attempt {
		case 3: // ~2m
			if delay < 1*time.Minute || delay > 3*time.Minute {
				t.Fatalf("attempt 3: expected ~2m, got %v", delay)
			}
		case 4: // ~5m
			if delay < 3*time.Minute || delay > 6*time.Minute {
				t.Fatalf("attempt 4: expected ~5m, got %v", delay)
			}
		case 5, 6, 7: // ~10m capped
			if delay < 8*time.Minute || delay > 11*time.Minute {
				t.Fatalf("attempt %d: expected ~10m capped (no reset), got %v", attempt, delay)
			}
		}
		lastState = currentState
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
