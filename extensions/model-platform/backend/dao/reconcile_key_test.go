package dao

import (
	"context"
	"errors"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
)

func TestReconcileDAO_KeyRotationLifecycle(t *testing.T) {
	db := setupReconcileTestDB(t)
	d := NewReconcileDAO()
	ctx := context.Background()
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-key", Status: entity.StatusReady, KeyHash: "old", Version: 1})

	dirty, err := d.MarkKeyDirty(ctx, db, "t-key", 1, time.Now())
	if err != nil || dirty.KeyDirtySince == nil || dirty.Version != 2 || dirty.Status != entity.StatusReady {
		t.Fatalf("expected a dirty ready record at version 2, got %+v (err=%v)", dirty, err)
	}
	if _, err := d.MarkKeyDirty(ctx, db, "t-key", 1, time.Now()); !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("a stale version must lose with ErrOptimisticLockConflict, got %v", err)
	}

	rotated, err := d.CommitKeyRotation(ctx, db, "t-key", 2, "alias-new", "new", "admin-1", time.Now())
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if rotated.KeyHash != "new" || rotated.GatewayKeyAlias != "alias-new" || rotated.KeyRotatedBy != "admin-1" ||
		rotated.KeyRotatedAt == nil || rotated.Version != 3 || rotated.KeyDirtySince == nil {
		t.Fatalf("unexpected rotated record: %+v", rotated)
	}

	// A caller that converged on the superseded key must not clear the marker of the current one
	if err := d.ClearKeyDirty(ctx, db, "t-key", "old"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if rec, _ := d.GetByTenantID(ctx, db, "t-key"); rec.KeyDirtySince == nil {
		t.Fatal("clearing for a superseded key hash must not clear the marker")
	}
	if err := d.ClearKeyDirty(ctx, db, "t-key", "new"); err != nil {
		t.Fatalf("clear failed: %v", err)
	}
	if rec, _ := d.GetByTenantID(ctx, db, "t-key"); rec.KeyDirtySince != nil {
		t.Fatal("clearing for the current key hash must clear the marker")
	}
}

func TestReconcileDAO_KeyUpdatesRequireAReadyRecord(t *testing.T) {
	db := setupReconcileTestDB(t)
	d := NewReconcileDAO()
	ctx := context.Background()
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-deleted", Status: entity.StatusDeleted, Version: 1})

	if _, err := d.MarkKeyDirty(ctx, db, "t-deleted", 1, time.Now()); !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("a decommissioned tenant must not be marked dirty, got %v", err)
	}
	if _, err := d.CommitKeyRotation(ctx, db, "t-deleted", 1, "a", "h", "op", time.Now()); !errors.Is(err, entity.ErrOptimisticLockConflict) {
		t.Fatalf("a decommissioned tenant must not accept a new key, got %v", err)
	}
}

func TestReconcileDAO_FindDirtyKeyTenants(t *testing.T) {
	db := setupReconcileTestDB(t)
	d := NewReconcileDAO()
	ctx := context.Background()
	now := time.Now()
	old, recent := now.Add(-10*time.Minute), now.Add(-time.Second)
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-old", Status: entity.StatusReady, KeyDirtySince: &old, Version: 1})
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-recent", Status: entity.StatusReady, KeyDirtySince: &recent, Version: 1})
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-clean", Status: entity.StatusReady, Version: 1})
	db.Create(&entity.TenantReconcileRecord{TenantID: "t-gone", Status: entity.StatusDeleted, KeyDirtySince: &old, Version: 1})

	got, err := d.FindDirtyKeyTenants(ctx, db, now.Add(-time.Minute), "", 10)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if len(got) != 1 || got[0] != "t-old" {
		t.Fatalf("expected only the ready tenant that stayed dirty, got %v", got)
	}

	for _, id := range []string{"t-old-b", "t-old-c"} {
		db.Create(&entity.TenantReconcileRecord{TenantID: id, Status: entity.StatusReady, KeyDirtySince: &old, Version: 1})
	}
	page, _ := d.FindDirtyKeyTenants(ctx, db, now.Add(-time.Minute), "", 2)
	next, _ := d.FindDirtyKeyTenants(ctx, db, now.Add(-time.Minute), page[len(page)-1], 2)
	if len(page) != 2 || page[0] != "t-old" || page[1] != "t-old-b" || len(next) != 1 || next[0] != "t-old-c" {
		t.Fatalf("expected pages ordered by tenant id that continue after the last one, got %v then %v", page, next)
	}
}
