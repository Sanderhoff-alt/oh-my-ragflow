package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func TestKeyRotator_ConcurrentChangeRevokesTheOrphanKey(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-conflict"
	oldKey := provisionReadyTenant(t, db, m, tenantID)

	// The record changes while the gateway issues the key, as a decommission or another rotation would do
	m.onGenerate = func() {
		db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).
			Update("version", gorm.Expr("version + 1"))
	}

	_, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin")
	if !errors.Is(err, ErrRotationConflict) {
		t.Fatalf("expected ErrRotationConflict, got %v", err)
	}
	if instanceKey(t, db, tenantID) != oldKey {
		t.Error("the instance must keep its key when the swap does not commit")
	}
	if len(m.generated) != 1 {
		t.Errorf("the key issued for the failed rotation must be revoked, %d keys remain", len(m.generated))
	}
	if _, ok := m.generated[oldKey]; !ok {
		t.Error("the current key must survive a failed rotation")
	}
}

func TestKeyRotator_RevocationFailureIsFinishedBySweep(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-revoke-fails"
	oldKey := provisionReadyTenant(t, db, m, tenantID)
	rotator := NewKeyRotator(m, nil)

	m.deleteErr = errors.New("gateway returned error status 500")
	res, err := rotator.Rotate(context.Background(), db, tenantID, "admin")
	if err != nil {
		t.Fatalf("a committed rotation must not fail when only the revocation does: %v", err)
	}
	if res.Converged {
		t.Fatal("the result must report that the old key may still be valid")
	}
	if instanceKey(t, db, tenantID) == oldKey {
		t.Fatal("the tenant must already use the new key")
	}
	if _, ok := m.generated[oldKey]; !ok {
		t.Fatal("the old key is expected to survive while the gateway rejects deletions")
	}
	rec, _ := dao.NewReconcileDAO().GetByTenantID(context.Background(), db, tenantID)
	if rec.KeyDirtySince == nil {
		t.Fatal("the dirty marker must stay until the old key is revoked")
	}

	m.deleteErr = nil
	if n, err := rotator.ConvergeDirtyTenants(context.Background(), db, 0, 10); err != nil || n != 1 {
		t.Fatalf("expected the sweep to converge 1 tenant, got %d (err=%v)", n, err)
	}
	if _, ok := m.generated[oldKey]; ok {
		t.Error("the sweep must revoke the old key")
	}
	rec, _ = dao.NewReconcileDAO().GetByTenantID(context.Background(), db, tenantID)
	if rec.KeyDirtySince != nil {
		t.Error("the sweep must clear the dirty marker")
	}
}

func TestKeyRotator_SweepSkipsRecentAndCleanTenants(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-sweep-skip"
	provisionReadyTenant(t, db, m, tenantID)
	rotator := NewKeyRotator(m, nil)

	if n, err := rotator.ConvergeDirtyTenants(context.Background(), db, 0, 10); err != nil || n != 0 {
		t.Fatalf("a clean tenant must not be swept, got %d (err=%v)", n, err)
	}

	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).
		Update("key_dirty_since", time.Now())
	if n, err := rotator.ConvergeDirtyTenants(context.Background(), db, time.Hour, 10); err != nil || n != 0 {
		t.Fatalf("a rotation that just started must be left alone, got %d (err=%v)", n, err)
	}
}

func TestKeyRotator_RejectsTenantsThatCannotRotate(t *testing.T) {
	db := setupTestDB(t)
	rotator := NewKeyRotator(newMockGatewayClient(), nil)
	ctx := context.Background()

	if _, err := rotator.Rotate(ctx, db, "bad tenant id!", "admin"); !errors.Is(err, ErrInvalidTenantID) {
		t.Errorf("expected ErrInvalidTenantID, got %v", err)
	}
	if _, err := rotator.Rotate(ctx, db, "tenant-unknown", "admin"); !errors.Is(err, entity.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}

	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-pending", Status: entity.StatusPending, Version: 1})
	if _, err := rotator.Rotate(ctx, db, "tenant-pending", "admin"); !errors.Is(err, ErrTenantNotReady) {
		t.Errorf("expected ErrTenantNotReady for a tenant without a managed instance, got %v", err)
	}
}

func TestKeyRotator_DecommissionedTenantCannotRotate(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-decommissioned"
	provisionReadyTenant(t, db, m, tenantID)
	if err := NewTenantDecommissioner(m).DecommissionTenant(context.Background(), db, tenantID); err != nil {
		t.Fatalf("decommission failed: %v", err)
	}

	_, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin")
	if !errors.Is(err, ErrTenantNotReady) {
		t.Fatalf("expected ErrTenantNotReady, got %v", err)
	}
	if len(m.generated) != 0 {
		t.Errorf("no key may be issued for a decommissioned tenant, got %d", len(m.generated))
	}
	var instance rentity.TenantModelInstance
	db.Where("instance_name = ?", PlatformInstanceName).First(&instance)
	if instance.APIKey != "" {
		t.Error("the scrubbed credential must stay empty")
	}
}
