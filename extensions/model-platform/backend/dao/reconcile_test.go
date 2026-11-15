package dao

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupReconcileTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := migration.MigrateReconcileSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to run migration: %v", err)
	}

	return db
}

func TestStateMachine_TransitionValidation(t *testing.T) {
	// 1. Valid transitions
	validCases := [][2]entity.ReconcileStatus{
		{entity.StatusPending, entity.StatusProvisioning},
		{entity.StatusPending, entity.StatusDeleted},
		{entity.StatusProvisioning, entity.StatusReady},
		{entity.StatusProvisioning, entity.StatusDegraded},
		{entity.StatusProvisioning, entity.StatusFailed},
		{entity.StatusProvisioning, entity.StatusDeleted},
		{entity.StatusReady, entity.StatusProvisioning},
		{entity.StatusReady, entity.StatusDeleted},
		{entity.StatusDegraded, entity.StatusProvisioning},
		{entity.StatusDegraded, entity.StatusFailed},
		{entity.StatusDegraded, entity.StatusDeleted},
		{entity.StatusFailed, entity.StatusProvisioning},
		{entity.StatusFailed, entity.StatusDeleted},
		// Self transitions (idempotent)
		{entity.StatusPending, entity.StatusPending},
		{entity.StatusReady, entity.StatusReady},
	}

	for _, tc := range validCases {
		if !entity.IsValidTransition(tc[0], tc[1]) {
			t.Errorf("expected transition %q -> %q to be VALID, but was rejected", tc[0], tc[1])
		}
		if err := entity.ValidateTransition(tc[0], tc[1]); err != nil {
			t.Errorf("ValidateTransition(%q, %q) returned error: %v", tc[0], tc[1], err)
		}
	}

	// 2. Invalid transitions
	invalidCases := [][2]entity.ReconcileStatus{
		{entity.StatusPending, entity.StatusReady},                             // cannot skip provisioning
		{entity.StatusPending, entity.StatusFailed},                            // cannot fail without attempt
		{entity.StatusReady, entity.StatusFailed},                              // ready model cannot fail directly
		{entity.StatusDeleted, entity.StatusPending},                           // deleted is terminal
		{entity.StatusDeleted, entity.StatusReady},                             // deleted is terminal
		{entity.StatusDeleted, entity.StatusProvisioning},                      // deleted is terminal
		{entity.StatusProvisioning, entity.StatusProvisioning},                 // cannot re-claim already provisioning
		{entity.ReconcileStatus("unknown"), entity.ReconcileStatus("unknown")}, // unknown status self-transition is invalid
		{entity.ReconcileStatus(""), entity.ReconcileStatus("")},               // empty status self-transition is invalid
	}

	for _, tc := range invalidCases {
		if entity.IsValidTransition(tc[0], tc[1]) {
			t.Errorf("expected transition %q -> %q to be INVALID, but was accepted", tc[0], tc[1])
		}
		if err := entity.ValidateTransition(tc[0], tc[1]); !errors.Is(err, entity.ErrInvalidStateTransition) {
			t.Errorf("expected ErrInvalidStateTransition for %q -> %q, got: %v", tc[0], tc[1], err)
		}
	}
}

func TestReconcileDAO_FullLifecycle(t *testing.T) {
	db := setupReconcileTestDB(t)
	dao := NewReconcileDAO()
	ctx := context.Background()
	tenantID := "tenant-lifecycle-1"

	// 1. Initial Insert (pending, version 1)
	initial := entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	}
	if err := db.Create(&initial).Error; err != nil {
		t.Fatalf("failed to insert initial record: %v", err)
	}

	// 2. ClaimForProvisioning -> provisioning, version 2
	rec, err := dao.ClaimForProvisioning(ctx, db, tenantID, 1)
	if err != nil {
		t.Fatalf("ClaimForProvisioning failed: %v", err)
	}
	if rec.Status != entity.StatusProvisioning || rec.Version != 2 {
		t.Errorf("expected provisioning, version 2; got %s, version %d", rec.Status, rec.Version)
	}

	// 3. MarkDegraded (simulating temporary gateway 503) -> degraded, version 3, retry_count 1
	retryAt := time.Now().Add(5 * time.Minute)
	rec, err = dao.MarkDegraded(ctx, db, tenantID, 2, "gateway connection refused", retryAt)
	if err != nil {
		t.Fatalf("MarkDegraded failed: %v", err)
	}
	if rec.Status != entity.StatusDegraded || rec.Version != 3 || rec.RetryCount != 1 {
		t.Errorf("expected degraded, version 3, retry 1; got %s, version %d, retry %d", rec.Status, rec.Version, rec.RetryCount)
	}
	if rec.LastError != "gateway connection refused" {
		t.Errorf("expected last_error recorded, got %q", rec.LastError)
	}

	// 4. FindDueRetries
	// Should not find if now is before retryAt
	due, err := dao.FindDueRetries(ctx, db, time.Now(), 10)
	if err != nil {
		t.Fatalf("FindDueRetries failed: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("expected 0 due records before retryAt, got %d", len(due))
	}

	// Should find if now is after retryAt
	due, err = dao.FindDueRetries(ctx, db, retryAt.Add(1*time.Second), 10)
	if err != nil {
		t.Fatalf("FindDueRetries failed: %v", err)
	}
	if len(due) != 1 || due[0].TenantID != tenantID {
		t.Errorf("expected 1 due record for %s, got %d", tenantID, len(due))
	}

	// 5. ClaimForProvisioning again -> provisioning, version 4
	rec, err = dao.ClaimForProvisioning(ctx, db, tenantID, 3)
	if err != nil {
		t.Fatalf("ClaimForProvisioning retry failed: %v", err)
	}
	if rec.Status != entity.StatusProvisioning || rec.Version != 4 {
		t.Errorf("expected provisioning, version 4; got %s, version %d", rec.Status, rec.Version)
	}

	// 6. MarkReady -> ready, version 5, retry_count reset to 0
	rec, err = dao.MarkReady(ctx, db, tenantID, 4, "tenant_tenant-lifecycle-1", "hash-abc-123")
	if err != nil {
		t.Fatalf("MarkReady failed: %v", err)
	}
	if rec.Status != entity.StatusReady || rec.Version != 5 || rec.RetryCount != 0 {
		t.Errorf("expected ready, version 5, retry 0; got %s, version %d, retry %d", rec.Status, rec.Version, rec.RetryCount)
	}
	if rec.GatewayKeyAlias != "tenant_tenant-lifecycle-1" || rec.KeyHash != "hash-abc-123" {
		t.Errorf("expected key alias and hash saved, got alias=%q, hash=%q", rec.GatewayKeyAlias, rec.KeyHash)
	}

	// 7. MarkDeleted -> deleted, version 6
	rec, err = dao.MarkDeleted(ctx, db, tenantID, 5)
	if err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	if rec.Status != entity.StatusDeleted || rec.Version != 6 {
		t.Errorf("expected deleted, version 6; got %s, version %d", rec.Status, rec.Version)
	}
}

func TestReconcileDAO_OptimisticLockConflict(t *testing.T) {
	db := setupReconcileTestDB(t)
	dao := NewReconcileDAO()
	ctx := context.Background()
	tenantID := "tenant-conflict-1"

	initial := entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	}
	db.Create(&initial)

	// Worker 1 claims with version 1 -> succeeds
	_, err := dao.ClaimForProvisioning(ctx, db, tenantID, 1)
	if err != nil {
		t.Fatalf("Worker 1 claim failed: %v", err)
	}

	// Worker 2 attempts to claim with stale version 1 -> must return ErrOptimisticLockConflict
	_, err = dao.ClaimForProvisioning(ctx, db, tenantID, 1)
	if !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("expected ErrOptimisticLockConflict for stale version 1, got: %v", err)
	}
}

func TestReconcileDAO_CannotClaimAlreadyProvisioning(t *testing.T) {
	db := setupReconcileTestDB(t)
	dao := NewReconcileDAO()
	ctx := context.Background()
	tenantID := "tenant-already-provisioning-1"

	initial := entity.TenantReconcileRecord{
		TenantID: tenantID,
		Status:   entity.StatusPending,
		Version:  1,
	}
	db.Create(&initial)

	// First claim succeeds, advancing status to provisioning and version to 2
	rec, err := dao.ClaimForProvisioning(ctx, db, tenantID, 1)
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if rec.Status != entity.StatusProvisioning || rec.Version != 2 {
		t.Fatalf("expected provisioning version 2, got %s version %d", rec.Status, rec.Version)
	}

	// Second claim with current version 2 MUST fail because record is already in provisioning
	_, err = dao.ClaimForProvisioning(ctx, db, tenantID, 2)
	if err == nil {
		t.Fatal("expected error claiming already-provisioning record, got nil")
	}
	if !errors.Is(err, entity.ErrInvalidStateTransition) {
		t.Fatalf("expected ErrInvalidStateTransition, got: %v", err)
	}
}

func TestReconcileDAO_ConcurrentOptimisticClaims(t *testing.T) {
	db := setupReconcileTestDB(t)
	dao := NewReconcileDAO()
	ctx := context.Background()
	tenantID := "tenant-concurrent-claim-1"

	initial := entity.TenantReconcileRecord{
		TenantID: tenantID,
		Status:   entity.StatusPending,
		Version:  1,
	}
	db.Create(&initial)

	const contenders = 10
	var successCount int32
	var conflictCount int32
	var wg sync.WaitGroup

	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := dao.ClaimForProvisioning(ctx, db, tenantID, 1)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
			} else if errors.Is(err, entity.ErrOptimisticLockConflict) {
				atomic.AddInt32(&conflictCount, 1)
			}
		}()
	}

	wg.Wait()

	// Exactly ONE worker must succeed, all others must get optimistic lock conflict
	if atomic.LoadInt32(&successCount) != 1 {
		t.Errorf("expected exactly 1 winner, got %d", successCount)
	}
	if atomic.LoadInt32(&conflictCount) != contenders-1 {
		t.Errorf("expected %d conflicts, got %d", contenders-1, conflictCount)
	}
}

func TestMigration_Idempotency(t *testing.T) {
	db := setupReconcileTestDB(t)
	ctx := context.Background()

	// Run migration a second and third time -> must be completely idempotent
	if err := migration.MigrateReconcileSchema(ctx, db); err != nil {
		t.Fatalf("second migration call failed: %v", err)
	}
	if err := migration.MigrateReconcileSchema(ctx, db); err != nil {
		t.Fatalf("third migration call failed: %v", err)
	}

	if err := migration.MigrateReconcileSchema(ctx, nil); !errors.Is(err, migration.ErrNilDB) {
		t.Errorf("expected ErrNilDB when passing nil DB, got: %v", err)
	}
}

func TestClaimDueForProvisioning_AtomicallyEnforcesDeadline(t *testing.T) {
	db := setupReconcileTestDB(t)
	ctx := context.Background()
	dao := NewReconcileDAO()

	tenantID := "tenant-claim-due-test"
	future := time.Now().Add(5 * time.Minute)
	past := time.Now().Add(-5 * time.Minute)

	// 1. Record with future deadline should fail to claim via atomic SQL predicate
	futureRec := &entity.TenantReconcileRecord{
		TenantID:    tenantID,
		Status:      entity.StatusDegraded,
		RetryCount:  1,
		MaxRetries:  50,
		NextRetryAt: &future,
		Version:     1,
	}
	db.Create(futureRec)

	_, err := dao.ClaimDueForProvisioning(ctx, db, tenantID, 1, time.Now())
	if !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("expected ErrOptimisticLockConflict for future deadline, got %v", err)
	}

	// 2. Simulate concurrent race: record had past deadline, but replica pushed next_retry_at into future
	// while version remains unchanged. The atomic SQL predicate must reject the claim.
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Updates(map[string]interface{}{
		"next_retry_at": past,
		"version":       2,
	})
	// Concurrent worker pushes deadline to future without bumping version:
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Update("next_retry_at", future)

	_, err = dao.ClaimDueForProvisioning(ctx, db, tenantID, 2, time.Now())
	if !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("expected ErrOptimisticLockConflict when next_retry_at rescheduled to future concurrently, got %v", err)
	}

	// 3. Update to past deadline -> atomic SQL predicate matches and claim succeeds
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Updates(map[string]interface{}{
		"next_retry_at": past,
		"version":       2,
	})

	claimed, err := dao.ClaimDueForProvisioning(ctx, db, tenantID, 2, time.Now())
	if err != nil {
		t.Fatalf("expected successful claim for past deadline, got %v", err)
	}
	if claimed.Status != entity.StatusProvisioning {
		t.Errorf("expected status provisioning, got %s", claimed.Status)
	}
	if claimed.Version != 3 {
		t.Errorf("expected version 3, got %d", claimed.Version)
	}
}
