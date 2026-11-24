package reconciler

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"

	"gorm.io/gorm"
)

// provisionReadyTenant provisions a tenant through the reconciler and returns the key it was issued.
func provisionReadyTenant(t *testing.T, db *gorm.DB, m *mockGatewayClient, tenantID string) string {
	t.Helper()
	createTestTenantInDB(db, tenantID)
	if err := NewTenantReconciler(m, "http://127.0.0.1:4000", nil).Reconcile(context.Background(), db, tenantID); err != nil {
		t.Fatalf("provisioning %s failed: %v", tenantID, err)
	}
	return instanceKey(t, db, tenantID)
}

func instanceKey(t *testing.T, db *gorm.DB, tenantID string) string {
	t.Helper()
	instance, err := findPlatformInstance(context.Background(), db, tenantID)
	if err != nil {
		t.Fatalf("no managed instance for %s: %v", tenantID, err)
	}
	return instance.APIKey
}

func issuedModels(m *mockGatewayClient, key string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	models := append([]string(nil), m.generated[key].Models...)
	sort.Strings(models)
	return models
}

func TestKeyRotator_RotateSwapsKeyAndRevokesTheOldOne(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-1"
	oldKey := provisionReadyTenant(t, db, m, tenantID)
	oldModels := issuedModels(m, oldKey)
	other := seedKey(m, "sk-other-tenant", "tenant-other")

	res, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin-1")
	if err != nil {
		t.Fatalf("rotate failed: %v", err)
	}
	newKey := instanceKey(t, db, tenantID)
	if newKey == oldKey || newKey == "" {
		t.Fatalf("expected a new key in the managed instance, old=%q new=%q", oldKey, newKey)
	}
	if !res.Converged || res.RevokedKeys != 1 || res.KeyHash != gateway.HashKey(newKey) {
		t.Errorf("unexpected rotation result: %+v", res)
	}
	if got := issuedModels(m, newKey); strings.Join(got, ",") != strings.Join(oldModels, ",") {
		t.Errorf("rotation must keep the model whitelist, was %v, now %v", oldModels, got)
	}

	if _, ok := m.generated[oldKey]; ok {
		t.Error("the old key must be revoked at the gateway")
	}
	if _, ok := m.generated["sk-other-tenant"]; !ok {
		t.Errorf("another tenant's key %s must not be touched", other)
	}
	if keys, _ := m.ListTeamKeys(context.Background(), tenantID); len(keys) != 1 {
		t.Errorf("the gateway team must hold exactly the new key, got %d", len(keys))
	}

	rec, _ := dao.NewReconcileDAO().GetByTenantID(context.Background(), db, tenantID)
	if rec.Status != entity.StatusReady || rec.KeyHash != gateway.HashKey(newKey) || rec.KeyDirtySince != nil {
		t.Errorf("record must be ready, point at the new key and be clean: %+v", rec)
	}
	if rec.KeyRotatedBy != "admin-1" || rec.KeyRotatedAt == nil || !strings.HasPrefix(rec.GatewayKeyAlias, "tenant_"+tenantID+"_") {
		t.Errorf("rotation must be attributed on the record: %+v", rec)
	}
}

func TestKeyRotator_RotateTwiceKeepsASingleKey(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-twice"
	provisionReadyTenant(t, db, m, tenantID)
	rotator := NewKeyRotator(m, nil)

	for i := 0; i < 3; i++ {
		if _, err := rotator.Rotate(context.Background(), db, tenantID, "admin"); err != nil {
			t.Fatalf("rotation %d failed: %v", i, err)
		}
	}
	if len(m.generated) != 1 {
		t.Fatalf("expected a single live key after three rotations, got %d", len(m.generated))
	}
}

func TestKeyRotator_GatewayFailureKeepsTheCurrentKey(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-gw-down"
	oldKey := provisionReadyTenant(t, db, m, tenantID)
	m.generateErr = errors.New("gateway unreachable")

	_, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin")
	if !errors.Is(err, ErrGatewayFailure) {
		t.Fatalf("expected ErrGatewayFailure, got %v", err)
	}
	if instanceKey(t, db, tenantID) != oldKey {
		t.Error("a failed issue must leave the tenant on its current key")
	}
	if _, ok := m.generated[oldKey]; !ok {
		t.Error("the current key must stay valid when no replacement exists")
	}

	// The marker is left behind, and the sweep clears it without revoking the current key
	m.generateErr = nil
	if n, err := NewKeyRotator(m, nil).ConvergeDirtyTenants(context.Background(), db, 0, 10); err != nil || n != 1 {
		t.Fatalf("expected the sweep to converge 1 tenant, got %d (err=%v)", n, err)
	}
	rec, _ := dao.NewReconcileDAO().GetByTenantID(context.Background(), db, tenantID)
	if rec.KeyDirtySince != nil {
		t.Error("the sweep must clear the dirty marker")
	}
	if _, ok := m.generated[oldKey]; !ok {
		t.Error("the sweep must not revoke the key the record still names")
	}
}
