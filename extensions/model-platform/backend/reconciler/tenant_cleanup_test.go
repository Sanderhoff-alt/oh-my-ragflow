package reconciler

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"
)

func TestTenantDecommissioner_DecommissionActiveTenant(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-1"

	// 1. Provision tenant
	createTestTenantInDB(db, tenantID)
	if err := rec.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify key was generated
	mockClient.mu.Lock()
	keysCount := len(mockClient.generated)
	mockClient.mu.Unlock()
	if keysCount != 1 {
		t.Fatalf("expected 1 generated key, got %d", keysCount)
	}

	// 2. Decommission tenant
	if err := decommissioner.DecommissionTenant(ctx, db, tenantID); err != nil {
		t.Fatalf("DecommissionTenant failed: %v", err)
	}

	// 3. Verify key was revoked from gateway
	mockClient.mu.Lock()
	deletedCount := len(mockClient.deletedKeys)
	remainingKeys := len(mockClient.generated)
	mockClient.mu.Unlock()

	if deletedCount != 1 {
		t.Errorf("expected 1 deleted key in gateway, got %d", deletedCount)
	}
	if remainingKeys != 0 {
		t.Errorf("expected 0 remaining keys in gateway, got %d", remainingKeys)
	}

	// 4. Verify Reconcile state is deleted
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDeleted {
		t.Errorf("expected status deleted, got %s", state.Status)
	}

	// 5. Verify instance is deactivated and API key is scrubbed
	var instance rentity.TenantModelInstance
	db.Where("instance_name = ?", PlatformInstanceName).First(&instance)
	if instance.Status != "deleted" {
		t.Errorf("expected instance status deleted, got %s", instance.Status)
	}
	if instance.APIKey != "" {
		t.Errorf("expected instance APIKey to be scrubbed empty, got %q", instance.APIKey)
	}

	// 6. Verify models are marked deleted
	var models []rentity.TenantModel
	db.Where("instance_id = ?", instance.ID).Find(&models)
	for _, m := range models {
		if m.Status != "deleted" {
			t.Errorf("model %s status is %s, expected deleted", m.ModelName, m.Status)
		}
	}
}

func TestTenantDecommissioner_Idempotency(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-idempotent"

	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Decommission 5 times consecutively
	for i := 0; i < 5; i++ {
		if err := decommissioner.DecommissionTenant(ctx, db, tenantID); err != nil {
			t.Fatalf("iteration %d failed: %v", i, err)
		}
	}

	mockClient.mu.Lock()
	deletedCount := len(mockClient.deletedKeys)
	mockClient.mu.Unlock()

	// Exactly 1 delete key call
	if deletedCount != 1 {
		t.Errorf("expected exactly 1 key delete invocation, got %d", deletedCount)
	}
}

func TestTenantDecommissioner_EventSubscription(t *testing.T) {
	db := setupTestDB(t)
	bus := event.NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	unsub := decommissioner.RegisterWithEventBus(bus, db)
	defer unsub()

	tenantID := "tenant-decom-event"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Publish TenantDeleted event
	bus.Publish(event.NewTenantDeletedEvent(tenantID))

	// Wait for event delivery and decommissioning
	deadline := time.Now().Add(2 * time.Second)
	rdao := dao.NewReconcileDAO()
	for time.Now().Before(deadline) {
		state, err := rdao.GetByTenantID(ctx, db, tenantID)
		if err == nil && state.Status == entity.StatusDeleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil || state.Status != entity.StatusDeleted {
		t.Fatalf("expected tenant to be deleted via event, got status: %v", state.Status)
	}

	mockClient.mu.Lock()
	deletedCount := len(mockClient.deletedKeys)
	mockClient.mu.Unlock()
	if deletedCount != 1 {
		t.Errorf("expected virtual key revoked, got deletedCount: %d", deletedCount)
	}
}

func TestTenantDecommissioner_ScanAndDecommissionOrphanedTenants(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)
	ctx := context.Background()

	// Provision 2 tenants
	t1 := "tenant-orphan-1"
	t2 := "tenant-orphan-2"
	createTestTenantInDB(db, t1)
	createTestTenantInDB(db, t2)
	_ = rec.Reconcile(ctx, db, t1)
	_ = rec.Reconcile(ctx, db, t2)

	// Soft-delete t1 (status = '0')
	db.Exec("UPDATE tenant SET status = '0' WHERE id = ?", t1)

	// Hard-delete t2
	db.Exec("DELETE FROM tenant WHERE id = ?", t2)

	// Run audit scanner
	decommissionedCount, err := decommissioner.ScanAndDecommissionOrphanedTenants(ctx, db)
	if err != nil {
		t.Fatalf("ScanAndDecommissionOrphanedTenants failed: %v", err)
	}
	if decommissionedCount != 2 {
		t.Errorf("expected 2 orphaned tenants decommissioned, got %d", decommissionedCount)
	}

	mockClient.mu.Lock()
	deletedCount := len(mockClient.deletedKeys)
	mockClient.mu.Unlock()
	if deletedCount != 2 {
		t.Errorf("expected 2 keys revoked from gateway, got %d", deletedCount)
	}
}

func TestTenantDecommissioner_GatewayOutage_PreservesStateForRetry(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-outage"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Simulate gateway outage
	mockClient.mu.Lock()
	mockClient.deleteErr = errors.New("gateway connection refused 503")
	mockClient.mu.Unlock()

	// Decommission attempt should fail and not destroy DB state
	err := decommissioner.DecommissionTenant(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected DecommissionTenant to fail when gateway is down, got nil")
	}

	// Verify state is NOT marked deleted and API key is NOT scrubbed
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil || state.Status == entity.StatusDeleted {
		t.Fatalf("state should not be deleted during outage, got %v", state.Status)
	}

	var instance rentity.TenantModelInstance
	db.Where("instance_name = ?", PlatformInstanceName).First(&instance)
	if instance.APIKey == "" {
		t.Fatal("API key must NOT be scrubbed if gateway deletion failed")
	}

	// Soft-delete tenant in upstream DB
	db.Exec("UPDATE tenant SET status = '0' WHERE id = ?", tenantID)

	// LiteLLM recovers
	mockClient.mu.Lock()
	mockClient.deleteErr = nil
	mockClient.mu.Unlock()

	// Background audit scan recovers and cleanly decommissions
	count, err := decommissioner.ScanAndDecommissionOrphanedTenants(ctx, db)
	if err != nil || count != 1 {
		t.Fatalf("expected audit scanner to heal tenant after recovery, count=%d, err=%v", count, err)
	}

	state, _ = rdao.GetByTenantID(ctx, db, tenantID)
	if state.Status != entity.StatusDeleted {
		t.Errorf("expected status deleted after recovery, got %s", state.Status)
	}
}

func TestTenantDecommissioner_RevokesByAliasWhenInstanceMissing(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-alias-fallback"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Hard delete instance row before decommissioner runs
	db.Exec("DELETE FROM tenant_model_instance")

	if err := decommissioner.DecommissionTenant(ctx, db, tenantID); err != nil {
		t.Fatalf("DecommissionTenant failed: %v", err)
	}

	mockClient.mu.Lock()
	deleted := append([]string(nil), mockClient.deletedKeys...)
	remaining := len(mockClient.generated)
	mockClient.mu.Unlock()

	if len(deleted) != 1 || deleted[0] != "tenant_"+tenantID {
		t.Errorf("expected key revoked by alias tenant_%s, got %v", tenantID, deleted)
	}
	if remaining != 0 {
		t.Errorf("expected gateway key to be gone, %d remaining", remaining)
	}
}

func TestTenantDecommissioner_RevokesByAliasIgnoringStoredCredential(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-stale-credential"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// A rotated or tampered local credential must not influence revocation
	db.Model(&rentity.TenantModelInstance{}).
		Where("instance_name = ?", PlatformInstanceName).
		Update("api_key", "sk-stale-local-credential")

	if err := decommissioner.DecommissionTenant(ctx, db, tenantID); err != nil {
		t.Fatalf("DecommissionTenant failed: %v", err)
	}

	mockClient.mu.Lock()
	deleted := append([]string(nil), mockClient.deletedKeys...)
	remaining := len(mockClient.generated)
	mockClient.mu.Unlock()

	if len(deleted) != 1 || deleted[0] != "tenant_"+tenantID {
		t.Errorf("expected key revoked by alias tenant_%s, got %v", tenantID, deleted)
	}
	if remaining != 0 {
		t.Errorf("expected gateway key to be gone, %d remaining", remaining)
	}

	var instance rentity.TenantModelInstance
	db.Where("instance_name = ?", PlatformInstanceName).First(&instance)
	if instance.APIKey != "" || instance.Status != "deleted" {
		t.Errorf("expected instance scrubbed and deleted, got api_key=%q status=%q", instance.APIKey, instance.Status)
	}
}

func TestTenantDecommissioner_GatewayKeyAlreadyAbsent_Decommissions(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-already-absent"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Key was already revoked out-of-band: gateway answers 404 for the alias
	mockClient.mu.Lock()
	mockClient.generated = make(map[string]*gateway.GenerateKeyRequest)
	mockClient.mu.Unlock()

	if err := decommissioner.DecommissionTenant(ctx, db, tenantID); err != nil {
		t.Fatalf("DecommissionTenant failed for already-absent key: %v", err)
	}

	state, err := dao.NewReconcileDAO().GetByTenantID(ctx, db, tenantID)
	if err != nil || state.Status != entity.StatusDeleted {
		t.Fatalf("expected status deleted, got %v (err=%v)", state, err)
	}
}

func TestTenantDecommissioner_NilGatewayClient_ReturnsConfigError(t *testing.T) {
	db := setupTestDB(t)
	decommissioner := NewTenantDecommissioner(nil)

	ctx := context.Background()
	tenantID := "tenant-decom-nil-client"
	createTestTenantInDB(db, tenantID)

	err := decommissioner.DecommissionTenant(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected error when gatewayClient is nil, got nil")
	}
}

func TestTenantDecommissioner_GatewayReturns500WithNotFoundText_DoesNotDecommission(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)

	ctx := context.Background()
	tenantID := "tenant-decom-500-not-found"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Simulate gateway returning 500 error whose body contains "not found"
	mockClient.mu.Lock()
	mockClient.deleteErr = errors.New("gateway returned error status 500: deployment model not found")
	mockClient.mu.Unlock()

	err := decommissioner.DecommissionTenant(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected DecommissionTenant to fail on 500 status containing 'not found', got nil")
	}

	// Verify DB state is NOT deleted and key is NOT scrubbed
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil || state.Status == entity.StatusDeleted {
		t.Fatalf("state must not be deleted on 500 error, got %v", state.Status)
	}

	var instance rentity.TenantModelInstance
	db.Where("instance_name = ?", PlatformInstanceName).First(&instance)
	if instance.APIKey == "" {
		t.Fatal("API key must NOT be scrubbed on 500 error")
	}
}

func TestTenantDecommissioner_ScanPaginatesBeyondBatchLimit(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)
	ctx := context.Background()

	// Provision 120 tenants
	const total = 120
	for i := 0; i < total; i++ {
		tid := fmt.Sprintf("tenant-page-%03d", i)
		createTestTenantInDB(db, tid)
		if err := rec.Reconcile(ctx, db, tid); err != nil {
			t.Fatalf("Reconcile failed for %s: %v", tid, err)
		}
	}

	// Soft-delete the last 20 tenants (indexes 100 to 119)
	for i := 100; i < total; i++ {
		tid := fmt.Sprintf("tenant-page-%03d", i)
		db.Exec("UPDATE tenant SET status = '0' WHERE id = ?", tid)
	}

	// Scan should paginate through and decommission all 20 orphaned tenants
	count, err := decommissioner.ScanAndDecommissionOrphanedTenants(ctx, db)
	if err != nil {
		t.Fatalf("ScanAndDecommissionOrphanedTenants failed: %v", err)
	}
	if count != 20 {
		t.Errorf("expected 20 orphaned tenants decommissioned across pagination batches, got %d", count)
	}
}

func TestTenantDecommissioner_ScanAggregatesErrorsWhenDecommissionFails(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	decommissioner := NewTenantDecommissioner(mockClient)
	ctx := context.Background()

	tenantID := "tenant-scan-err-agg"
	createTestTenantInDB(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Soft-delete tenant in upstream DB
	db.Exec("UPDATE tenant SET status = '0' WHERE id = ?", tenantID)

	// Simulate gateway outage during audit scan
	mockClient.mu.Lock()
	mockClient.deleteErr = errors.New("gateway connection refused 503")
	mockClient.mu.Unlock()

	count, err := decommissioner.ScanAndDecommissionOrphanedTenants(ctx, db)
	if err == nil {
		t.Fatal("expected audit scanner to return aggregated error when decommissioning fails, got nil")
	}
	if count != 0 {
		t.Errorf("expected 0 decommissioned when gateway fails, got %d", count)
	}
}
