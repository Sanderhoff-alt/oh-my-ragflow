package reconciler

import (
	"context"
	"errors"
	"testing"

	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func createTestKnowledgebase(db *gorm.DB, kbID, tenantID, embdID, tenantEmbdID, status string) {
	statusPtr := status
	var tEmbdIDPtr *string
	if tenantEmbdID != "" {
		tEmbdIDPtr = &tenantEmbdID
	}
	kb := rentity.Knowledgebase{
		ID:           kbID,
		TenantID:     tenantID,
		UserID:       "user-" + tenantID,
		Name:         "kb-" + kbID,
		EmbdID:       embdID,
		TenantEmbdID: tEmbdIDPtr,
		Status:       &statusPtr,
	}
	db.Create(&kb)
}

func TestEmbeddingLifecycle_RetireWithActiveKB_EntersDeprecated(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-embd-active-kb"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	embdModelName := DefaultEmbeddingModelName
	compositeID := embdModelName + "@" + PlatformInstanceName + "@" + ProviderOpenAIAPICompatible

	// Create 1 active knowledgebase referencing this embedding model
	createTestKnowledgebase(db, "kb-1", tenantID, compositeID, "", "1")

	// Retire embedding model
	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel failed: %v", err)
	}

	if res.State != EmbeddingStateDeprecated {
		t.Errorf("expected state deprecated, got %s", res.State)
	}
	if res.ActiveKBCount != 1 {
		t.Errorf("expected active KB count 1, got %d", res.ActiveKBCount)
	}
	if !res.AllowedInGateway {
		t.Error("expected AllowedInGateway=true for deprecated model")
	}

	// Verify TenantModel in DB
	var tm rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tm)
	if tm.Status != "active" {
		t.Errorf("model status must remain 'active' so existing KBs can query it, got %s", tm.Status)
	}
	if !isDeprecated(tm.Extra) {
		t.Errorf("model extra must contain deprecated: true, got %s", tm.Extra)
	}

	// Verify gateway allowed models still contains embedding model
	mockClient.mu.Lock()
	var clientKeyModels []string
	for _, gen := range mockClient.generated {
		clientKeyModels = gen.Models
	}
	mockClient.mu.Unlock()

	found := false
	for _, m := range clientKeyModels {
		if m == embdModelName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("model %s was prematurely removed from gateway key whitelist", embdModelName)
	}
}

func TestEmbeddingLifecycle_TenantEmbdIDReference_PreventsTombstone(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-embd-ref-id"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	var tm rentity.TenantModel
	db.Where("model_name = ?", DefaultEmbeddingModelName).First(&tm)

	// Create active KB referencing by tenant_embd_id only
	createTestKnowledgebase(db, "kb-by-id", tenantID, "legacy-custom-name", tm.ID, "1")

	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, DefaultEmbeddingModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel failed: %v", err)
	}
	if res.State != EmbeddingStateDeprecated {
		t.Fatalf("expected state deprecated when referenced by tenant_embd_id, got %s", res.State)
	}
	if res.ActiveKBCount != 1 {
		t.Errorf("expected active KB count 1, got %d", res.ActiveKBCount)
	}
}

func TestEmbeddingLifecycle_RetireWithZeroKB_EntersTombstoned(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-embd-zero-kb"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	embdModelName := DefaultEmbeddingModelName

	// 0 knowledgebases created
	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel failed: %v", err)
	}

	if res.State != EmbeddingStateTombstoned {
		t.Errorf("expected state tombstoned, got %s", res.State)
	}
	if res.ActiveKBCount != 0 {
		t.Errorf("expected active KB count 0, got %d", res.ActiveKBCount)
	}
	if res.AllowedInGateway {
		t.Error("expected AllowedInGateway=false for tombstoned model")
	}

	// Verify TenantModel in DB
	var tm rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tm)
	if tm.Status != "disabled" {
		t.Errorf("expected model status disabled, got %s", tm.Status)
	}

	// Verify gateway allowed models removed the retired model
	mockClient.mu.Lock()
	var clientKeyModels []string
	for _, gen := range mockClient.generated {
		clientKeyModels = gen.Models
	}
	mockClient.mu.Unlock()

	for _, m := range clientKeyModels {
		if m == embdModelName {
			t.Errorf("tombstoned model %s should be removed from gateway models, but was found", embdModelName)
		}
	}
}

func TestEmbeddingLifecycle_GatewayFailure_DoesNotTombstoneInDB(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-gateway-fail"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	// Simulate gateway outage
	mockClient.mu.Lock()
	mockClient.updateErr = errors.New("gateway connection refused 503")
	mockClient.mu.Unlock()

	_, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, DefaultEmbeddingModelName)
	if err == nil {
		t.Fatal("expected error when gateway update fails, got nil")
	}

	// Model must remain active in DB so background sweeps can retry
	var tm rentity.TenantModel
	db.Where("model_name = ?", DefaultEmbeddingModelName).First(&tm)
	if tm.Status != "active" {
		t.Fatalf("model status should not be disabled when gateway update fails, got %s", tm.Status)
	}
}

func TestEmbeddingLifecycle_SoftDeletedKBDoesNotBlockTombstone(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-soft-del-kb"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	embdModelName := DefaultEmbeddingModelName
	compositeID := embdModelName + "@" + PlatformInstanceName + "@" + ProviderOpenAIAPICompatible

	// Knowledgebase is soft-deleted (status = "0")
	createTestKnowledgebase(db, "kb-soft-del", tenantID, compositeID, "", "0")

	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel failed: %v", err)
	}

	if res.State != EmbeddingStateTombstoned {
		t.Errorf("soft deleted KB should not block tombstone, expected tombstoned got %s", res.State)
	}
}

func TestEmbeddingLifecycle_SweepTombstoneCandidates(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-sweep"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	embdModelName := DefaultEmbeddingModelName
	compositeID := embdModelName + "@" + PlatformInstanceName + "@" + ProviderOpenAIAPICompatible

	// Active KB -> model enters deprecated
	createTestKnowledgebase(db, "kb-sweep-1", tenantID, compositeID, "", "1")
	res, _ := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if res.State != EmbeddingStateDeprecated {
		t.Fatalf("expected state deprecated, got %s", res.State)
	}

	// First sweep: active KB still exists -> 0 tombstoned
	tombstoned, err := lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil || tombstoned != 0 {
		t.Fatalf("expected 0 tombstoned on first sweep, got %d (err=%v)", tombstoned, err)
	}

	// Now delete the active KB
	db.Exec("UPDATE knowledgebase SET status = '0' WHERE id = ?", "kb-sweep-1")

	// Second sweep: 0 active KBs remain -> model tombstoned!
	tombstoned, err = lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil || tombstoned != 1 {
		t.Fatalf("expected 1 tombstoned on second sweep, got %d (err=%v)", tombstoned, err)
	}

	var tm rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tm)
	if tm.Status != "disabled" {
		t.Errorf("model should be disabled after sweep, got %s", tm.Status)
	}

	// Third sweep: idempotent
	tombstoned, err = lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil || tombstoned != 0 {
		t.Fatalf("expected 0 on third sweep, got %d", tombstoned)
	}
}

func TestEmbeddingLifecycle_FilterAvailableForCreation(t *testing.T) {
	models := []rentity.TenantModel{
		{ID: "m1", ModelName: "active-embd", Status: "active", Extra: `{"is_managed":true}`},
		{ID: "m2", ModelName: "deprecated-embd", Status: "active", Extra: `{"is_managed":true,"deprecated":true,"read_only":true}`},
		{ID: "m3", ModelName: "disabled-embd", Status: "disabled", Extra: `{"is_managed":true,"tombstoned":true}`},
		{ID: "m4", ModelName: "active-chat", Status: "active", Extra: `{}`},
	}

	filtered := FilterAvailableForCreation(models)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 available models (m1, m4), got %d", len(filtered))
	}
	if filtered[0].ID != "m1" || filtered[1].ID != "m4" {
		t.Errorf("unexpected filtered models: %+v", filtered)
	}
}

func TestEmbeddingLifecycle_DeleteModelPermanently(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-perm-delete"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-del",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-del",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-del-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-to-delete"
	tm := rentity.TenantModel{
		ID:         "tm-del-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true}`,
	}
	db.Create(&tm)

	// 1. Cannot delete when active knowledgebase exists
	statusValid := "1"
	kb := rentity.Knowledgebase{
		ID:       "kb-del-active",
		TenantID: tenantID,
		EmbdID:   modelName,
		Status:   &statusValid,
	}
	db.Create(&kb)

	err := lifecycle.DeleteModelPermanently(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error when deleting model with active knowledgebase, got nil")
	}

	// 2. Delete knowledgebase, now delete succeeds
	db.Delete(&kb)

	err = lifecycle.DeleteModelPermanently(ctx, db, tenantID, modelName)
	if err != nil {
		t.Fatalf("expected delete to succeed when 0 knowledgebases, got %v", err)
	}

	// Verify model record is gone
	var count int64
	db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Count(&count)
	if count != 0 {
		t.Fatalf("expected model to be deleted from DB, but still found %d rows", count)
	}
}

func TestEmbeddingLifecycle_ParseExtraMap_HandlesNull(t *testing.T) {
	m := parseExtraMap("null")
	if m == nil {
		t.Fatal("expected non-nil map for JSON null")
	}
	// Safe to write without panic
	m["deprecated"] = true
	if !m["deprecated"].(bool) {
		t.Fatal("expected deprecated flag to be set")
	}

	m2 := parseExtraMap("")
	if m2 == nil {
		t.Fatal("expected non-nil map for empty string")
	}
}

func TestEmbeddingLifecycle_CheckEmbeddingUsage_ContextError(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err := lifecycle.CheckEmbeddingUsage(ctx, db, "tenant-cancel", "model-cancel")
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
}
