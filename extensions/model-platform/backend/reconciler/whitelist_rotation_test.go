package reconciler

import (
	"context"
	"testing"

	rentity "ragflow/internal/entity"
)

// A model going live loads the managed instance, then waits for the whitelist lock. A rotation that holds the
// lock in the meantime replaces the key, so the whitelist update must go to the key the instance holds after
// the wait, not to the one it held when it was loaded.
func TestIncludeModelInWhitelist_UsesTheKeyOfTheInstanceAfterARotation(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-whitelist-after-rotation"
	provisionReadyTenant(t, db, m, tenantID)

	stale, err := findPlatformInstance(context.Background(), db, tenantID)
	if err != nil {
		t.Fatalf("no managed instance: %v", err)
	}
	if _, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin"); err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	newKey := instanceKey(t, db, tenantID)
	if stale.APIKey == newKey {
		t.Fatal("the rotation must have replaced the key")
	}

	if err := IncludeModelInWhitelist(context.Background(), db, m, stale, "gpt-4o", int(rentity.ModelTypeChat)); err != nil {
		t.Fatalf("whitelist update failed: %v", err)
	}
	if m.lastUpdateKeyReq == nil || m.lastUpdateKeyReq.Key != newKey {
		t.Fatalf("expected the update to target the rotated key %q, got %+v", newKey, m.lastUpdateKeyReq)
	}
}
