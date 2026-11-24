package reconciler

import (
	"context"
	"errors"
	"testing"

	"ragflow/extensions/model-platform/backend/gateway"
)

func seedKey(m *mockGatewayClient, key, teamID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generated[key] = &gateway.GenerateKeyRequest{KeyName: "alias-" + key, TeamID: teamID}
	return gateway.HashKey(key)
}

func TestConvergeTenantKeys_RevokesEveryKeyExceptTheCurrentOne(t *testing.T) {
	m := newMockGatewayClient()
	current := seedKey(m, "sk-current", "t1")
	seedKey(m, "sk-old", "t1")
	seedKey(m, "sk-orphan", "t1")
	seedKey(m, "sk-other-tenant", "t2")

	n, err := ConvergeTenantKeys(context.Background(), m, "t1", current)
	if err != nil {
		t.Fatalf("converge failed: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 keys revoked, got %d", n)
	}
	for _, survivor := range []string{"sk-current", "sk-other-tenant"} {
		if _, ok := m.generated[survivor]; !ok {
			t.Errorf("%s must survive the convergence of t1", survivor)
		}
	}
	if len(m.generated) != 2 {
		t.Errorf("expected exactly the current key and the other tenant's key, got %d keys", len(m.generated))
	}
}

func TestConvergeTenantKeys_IsIdempotent(t *testing.T) {
	m := newMockGatewayClient()
	current := seedKey(m, "sk-current", "t1")
	seedKey(m, "sk-old", "t1")

	for i := 0; i < 3; i++ {
		if _, err := ConvergeTenantKeys(context.Background(), m, "t1", current); err != nil {
			t.Fatalf("converge #%d failed: %v", i, err)
		}
	}
	if len(m.deletedKeys) != 1 {
		t.Errorf("expected the stale key to be deleted exactly once, got %v", m.deletedKeys)
	}
}

func TestConvergeTenantKeys_RefusesWithoutCurrentKeyHash(t *testing.T) {
	m := newMockGatewayClient()
	seedKey(m, "sk-only", "t1")

	_, err := ConvergeTenantKeys(context.Background(), m, "t1", "")
	if !errors.Is(err, ErrNoCurrentKey) {
		t.Fatalf("expected ErrNoCurrentKey, got %v", err)
	}
	if len(m.generated) != 1 {
		t.Fatal("converging without a current hash must not revoke the only key")
	}
}

func TestConvergeTenantKeys_ReportsGatewayFailures(t *testing.T) {
	m := newMockGatewayClient()
	current := seedKey(m, "sk-current", "t1")
	seedKey(m, "sk-old", "t1")

	m.listErr = errors.New("gateway unreachable")
	if _, err := ConvergeTenantKeys(context.Background(), m, "t1", current); err == nil {
		t.Fatal("expected the list failure to be reported")
	}

	m.listErr = nil
	m.deleteErr = errors.New("gateway returned error status 500")
	if _, err := ConvergeTenantKeys(context.Background(), m, "t1", current); err == nil {
		t.Fatal("expected the revoke failure to be reported")
	}
	if len(m.generated) != 2 {
		t.Fatal("a failed revocation must leave the keys in place for the next attempt")
	}
}

func TestRevokeTenantKeys_RevokesTheWholeTeam(t *testing.T) {
	m := newMockGatewayClient()
	seedKey(m, "sk-a", "t1")
	seedKey(m, "sk-b", "t1")
	seedKey(m, "sk-c", "t2")

	n, err := RevokeTenantKeys(context.Background(), m, "t1")
	if err != nil || n != 2 {
		t.Fatalf("expected 2 revoked keys, got %d (err=%v)", n, err)
	}
	if _, ok := m.generated["sk-c"]; !ok || len(m.generated) != 1 {
		t.Fatalf("only the other tenant's key may remain, got %v", m.generated)
	}
}

func TestReconcile_RevokesKeysLeftByFailedAttempts(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	rec := NewTenantReconciler(m, "http://127.0.0.1:4000", nil)
	tenantID := "tenant-leftover"

	// A previous attempt issued a key, then crashed before it was recorded
	seedKey(m, "sk-leftover", tenantID)

	if err := rec.Reconcile(context.Background(), db, tenantID); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if _, stillThere := m.generated["sk-leftover"]; stillThere {
		t.Error("the leftover key must be revoked before the tenant's key is issued")
	}
	if len(m.generated) != 1 {
		t.Errorf("expected exactly the tenant's own key, got %d keys", len(m.generated))
	}
}
