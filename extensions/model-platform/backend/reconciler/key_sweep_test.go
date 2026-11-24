package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
)

func TestConvergeTenantKeys_RefusesWhenTheCurrentKeyIsNotListed(t *testing.T) {
	m := newMockGatewayClient()
	seedKey(m, "sk-listed-a", "t1")
	seedKey(m, "sk-listed-b", "t1")

	_, err := ConvergeTenantKeys(context.Background(), m, "t1", gateway.HashKey("sk-not-in-the-gateway"))
	if !errors.Is(err, ErrCurrentKeyNotListed) {
		t.Fatalf("expected ErrCurrentKeyNotListed, got %v", err)
	}
	if len(m.generated) != 2 || len(m.deletedKeys) != 0 {
		t.Fatalf("no key may be revoked when the current one is unknown, got %d keys left", len(m.generated))
	}
}

func TestKeyRotator_SweepReachesEveryDirtyTenantEvenWhenOneKeepsFailing(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	rotator := NewKeyRotator(m, nil)
	ctx := context.Background()

	// "tenant-a" sorts first and has no managed instance, so it can never converge
	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-a", Status: entity.StatusReady, KeyHash: "h", Version: 1})
	for _, id := range []string{"tenant-b", "tenant-c"} {
		provisionReadyTenant(t, db, m, id)
		seedKey(m, "sk-stale-"+id, id)
		db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", id).Update("key_dirty_since", time.Now().Add(-time.Hour))
	}
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", "tenant-a").Update("key_dirty_since", time.Now().Add(-time.Hour))

	n, err := rotator.ConvergeDirtyTenants(ctx, db, time.Minute, 1)
	if err != nil || n != 2 {
		t.Fatalf("expected the two healthy tenants to converge with a page size of one, got %d (err=%v)", n, err)
	}
	for _, id := range []string{"tenant-b", "tenant-c"} {
		if _, ok := m.generated["sk-stale-"+id]; ok {
			t.Errorf("the stale key of %s must be revoked", id)
		}
	}
}

func TestKeyRotator_ConvergeTenantReportsAMissingInstance(t *testing.T) {
	db := setupTestDB(t)
	rotator := NewKeyRotator(newMockGatewayClient(), nil)
	dirty := time.Now().Add(-time.Hour)
	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-no-instance", Status: entity.StatusReady, KeyHash: "h", KeyDirtySince: &dirty, Version: 1})

	if err := rotator.ConvergeTenant(context.Background(), db, "tenant-no-instance"); !errors.Is(err, ErrTenantNotReady) {
		t.Fatalf("a dirty tenant that cannot converge must be reported, got %v", err)
	}
}
