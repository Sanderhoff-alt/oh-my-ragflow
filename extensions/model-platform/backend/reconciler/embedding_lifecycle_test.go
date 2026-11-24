package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func createTestKnowledgebase(t *testing.T, db *gorm.DB, kbID, tenantID, embdID, tenantEmbdID, status string) {
	t.Helper()
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
	if err := db.Create(&kb).Error; err != nil {
		t.Fatalf("failed to create test knowledgebase %s: %v", kbID, err)
	}
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
	createTestKnowledgebase(t, db, "kb-1", tenantID, compositeID, "", "1")

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
	if !res.Transitioned {
		t.Error("expected Transitioned=true on initial deprecation")
	}

	// Verify TenantModel in DB immediately after initial retirement
	var tm rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tm)
	if tm.Status != "active" {
		t.Errorf("model status must remain 'active' so existing KBs can query it, got %s", tm.Status)
	}
	extraMap := parseExtraMap(tm.Extra)
	if extraMap["deprecated"] != true || extraMap["read_only"] != true {
		t.Errorf("model extra must contain deprecated: true and read_only: true, got %s", tm.Extra)
	}

	// Verify gateway allowed models immediately after initial retirement
	mockClient.mu.Lock()
	var clientKeyModelsInitial []string
	for _, gen := range mockClient.generated {
		clientKeyModelsInitial = gen.Models
	}
	mockClient.mu.Unlock()
	foundInitial := false
	for _, m := range clientKeyModelsInitial {
		if m == embdModelName {
			foundInitial = true
			break
		}
	}
	if !foundInitial {
		t.Errorf("model %s was prematurely removed from gateway key whitelist after initial retirement", embdModelName)
	}

	// Repeated retirement on already-deprecated model should succeed idempotently with Transitioned=false
	res2, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("repeated RetireEmbeddingModel failed: %v", err)
	}
	if res2.State != EmbeddingStateDeprecated {
		t.Errorf("expected state deprecated on repeat, got %s", res2.State)
	}
	if res2.Transitioned {
		t.Error("expected Transitioned=false on repeated retirement of already-deprecated model")
	}

	// Verify TenantModel in DB remains active and read-only after repeat call
	var tmRepeat rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tmRepeat)
	if tmRepeat.Status != "active" {
		t.Errorf("model status must remain 'active' after repeat retirement, got %s", tmRepeat.Status)
	}
	extraMapRepeat := parseExtraMap(tmRepeat.Extra)
	if extraMapRepeat["deprecated"] != true || extraMapRepeat["read_only"] != true {
		t.Errorf("model extra must preserve deprecated: true and read_only: true on repeat, got %s", tmRepeat.Extra)
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
	createTestKnowledgebase(t, db, "kb-by-id", tenantID, "legacy-custom-name", tm.ID, "1")

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
	extraMap := parseExtraMap(tm.Extra)
	if extraMap["tombstoned"] != true {
		t.Errorf("expected tombstoned: true in extra, got %s", tm.Extra)
	}

	// Verify gateway allowed models removed the retired model but preserves platform/chat
	mockClient.mu.Lock()
	var clientKeyModels []string
	for _, gen := range mockClient.generated {
		clientKeyModels = gen.Models
	}
	mockClient.mu.Unlock()

	hasChat := false
	for _, m := range clientKeyModels {
		if m == "platform/chat" {
			hasChat = true
		}
		if m == embdModelName {
			t.Errorf("tombstoned model %s should be removed from gateway models, but was found", embdModelName)
		}
		if m == "platform/embedding" {
			t.Errorf("platform/embedding should be removed when all embedding models are tombstoned, but was found")
		}
	}
	if !hasChat {
		t.Error("expected platform/chat to be preserved in gateway models after embedding retirement")
	}

	// Repeated retirement on already-tombstoned model should return EmbeddingStateTombstoned
	res2, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("repeated retirement failed: %v", err)
	}
	if res2.State != EmbeddingStateTombstoned {
		t.Errorf("expected State=%s on repeated retirement, got %s", EmbeddingStateTombstoned, res2.State)
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

	var origTM rentity.TenantModel
	db.Where("model_name = ?", DefaultEmbeddingModelName).First(&origTM)

	// Simulate gateway outage
	gwErr := errors.New("gateway connection refused 503")
	mockClient.mu.Lock()
	mockClient.updateErr = gwErr
	mockClient.mu.Unlock()

	_, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, DefaultEmbeddingModelName)
	if err == nil {
		t.Fatal("expected error when gateway update fails, got nil")
	}
	if !errors.Is(err, gwErr) {
		t.Fatalf("expected error wrapping %v, got %v", gwErr, err)
	}

	// Model must remain active in DB with unchanged metadata so subsequent retirement attempts can retry
	var tm rentity.TenantModel
	db.Where("model_name = ?", DefaultEmbeddingModelName).First(&tm)
	if tm.Status != "active" {
		t.Fatalf("model status should not be disabled when gateway update fails, got %s", tm.Status)
	}
	if tm.Extra != origTM.Extra {
		t.Fatalf("expected tm.Extra to remain unchanged %q, got %q", origTM.Extra, tm.Extra)
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
	createTestKnowledgebase(t, db, "kb-soft-del", tenantID, compositeID, "", "0")

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
	createTestKnowledgebase(t, db, "kb-sweep-1", tenantID, compositeID, "", "1")
	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("setup RetireEmbeddingModel failed: %v", err)
	}
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

func TestEmbeddingLifecycle_ReactivateEmbeddingModel(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-reactivate"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-reactivate",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-reactivate",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-reactivate-key",
		Status:       "active",
	}
	db.Create(&instance)

	// 1. Reactivate from Deprecated
	tmDeprecated := rentity.TenantModel{
		ID:         "tm-dep-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  "embd-deprecated",
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true,"read_only":true}`,
	}
	db.Create(&tmDeprecated)

	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, tmDeprecated.ModelName)
	if err != nil {
		t.Fatalf("expected reactivate from deprecated to succeed, got %v", err)
	}

	var updatedDep rentity.TenantModel
	db.First(&updatedDep, "id = ?", tmDeprecated.ID)
	if updatedDep.Status != "active" {
		t.Errorf("expected status 'active', got %q", updatedDep.Status)
	}
	extraDep := parseExtraMap(updatedDep.Extra)
	if extraDep["deprecated"] == true || extraDep["read_only"] == true {
		t.Errorf("expected deprecated and read_only flags to be cleared, got %+v", extraDep)
	}

	// 2. Reactivate from Tombstoned
	// Seed mockClient.generated so UpdateKey updates the stored key state
	mockClient.mu.Lock()
	mockClient.generated[instance.APIKey] = &gateway.GenerateKeyRequest{
		KeyName: "initial-test-key",
		Models:  []string{"platform/chat"},
	}
	mockClient.mu.Unlock()

	tmTombstoned := rentity.TenantModel{
		ID:         "tm-tomb-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  "embd-tombstoned",
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"deprecated":true,"tombstoned":true}`,
	}
	db.Create(&tmTombstoned)

	err = lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, tmTombstoned.ModelName)
	if err != nil {
		t.Fatalf("expected reactivate from tombstoned to succeed, got %v", err)
	}

	var updatedTomb rentity.TenantModel
	db.First(&updatedTomb, "id = ?", tmTombstoned.ID)
	if updatedTomb.Status != "active" {
		t.Errorf("expected status 'active', got %q", updatedTomb.Status)
	}
	extraTomb := parseExtraMap(updatedTomb.Extra)
	if extraTomb["tombstoned"] == true || extraTomb["deprecated"] == true {
		t.Errorf("expected tombstoned and deprecated flags to be cleared, got %+v", extraTomb)
	}

	// Verify gateway virtual key whitelist was updated to include the reactivated model
	mockClient.mu.Lock()
	lastReq := mockClient.lastUpdateKeyReq
	genKey := mockClient.generated[instance.APIKey]
	mockClient.mu.Unlock()
	if lastReq == nil {
		t.Fatal("expected gateway update key request, got nil")
	}
	found := false
	for _, m := range lastReq.Models {
		if m == tmTombstoned.ModelName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected reactivated model %s in gateway models, got %v", tmTombstoned.ModelName, lastReq.Models)
	}
	if genKey == nil {
		t.Fatal("expected stored gateway key to exist")
	}
	genFound := false
	for _, m := range genKey.Models {
		if m == tmTombstoned.ModelName {
			genFound = true
			break
		}
	}
	if !genFound {
		t.Fatalf("expected reactivated model %s in stored gateway key models, got %v", tmTombstoned.ModelName, genKey.Models)
	}

	// 3. Reactivating an already active model also idempotently re-synchronizes gateway.
	// Simulate gateway key drift by resetting key models without the embedding model.
	mockClient.mu.Lock()
	mockClient.lastUpdateKeyReq = nil
	mockClient.generated[instance.APIKey] = &gateway.GenerateKeyRequest{
		KeyName: "drifted-key",
		Models:  []string{"platform/chat"},
	}
	mockClient.mu.Unlock()

	err = lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, tmTombstoned.ModelName)
	if err != nil {
		t.Fatalf("expected idempotent reactivate to succeed, got %v", err)
	}
	mockClient.mu.Lock()
	lastReq2 := mockClient.lastUpdateKeyReq
	genKey2 := mockClient.generated[instance.APIKey]
	mockClient.mu.Unlock()
	if lastReq2 == nil {
		t.Fatal("expected gateway update key request on already active model, got nil")
	}
	if genKey2 == nil {
		t.Fatal("expected stored gateway key to exist after idempotent reactivate")
	}
	driftRestored := false
	for _, m := range genKey2.Models {
		if m == tmTombstoned.ModelName {
			driftRestored = true
			break
		}
	}
	if !driftRestored {
		t.Fatalf("expected idempotent reactivate to restore model %s in stored gateway key, got %v", tmTombstoned.ModelName, genKey2.Models)
	}
}

func TestEmbeddingLifecycle_Reactivate_ContextCanceled_CompensatesGateway(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-react-cancel"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-react-cancel",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-react-cancel",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-react-cancel-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-react-cancel"
	tm := rentity.TenantModel{
		ID:         "tm-react-cancel-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"is_managed":true,"deprecated":true,"tombstoned":true}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-react-cancel-key"] = &gateway.GenerateKeyRequest{
		Models: []string{"platform/chat"},
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	lifecycle.onBeforeReactivateDB = func(tID, mName string) {
		mockClient.mu.Lock()
		stored := mockClient.generated["sk-react-cancel-key"]
		mockClient.mu.Unlock()
		hasM := false
		if stored != nil {
			for _, m := range stored.Models {
				if m == mName {
					hasM = true
					break
				}
			}
		}
		if !hasM {
			t.Errorf("expected gateway key to have model %s prior to cancellation, got %v", mName, stored)
		}
		cancel()
	}

	err := lifecycle.ReactivateEmbeddingModel(cancelCtx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error due to canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected error wrapping context.Canceled, got %v", err)
	}

	// Gateway compensation must succeed using independent rollback context and exclude modelName
	mockClient.mu.Lock()
	storedKey := mockClient.generated["sk-react-cancel-key"]
	mockClient.mu.Unlock()
	if storedKey == nil {
		t.Fatal("expected stored key in gateway, got nil")
	}
	if len(storedKey.Models) != 1 || storedKey.Models[0] != "platform/chat" {
		t.Fatalf("expected gateway key models to be exactly [platform/chat] after rollback, got %v", storedKey.Models)
	}

	// Model must remain disabled in database
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "disabled" {
		t.Fatalf("model status should remain disabled after rollback, got %s", verifyTM.Status)
	}
}

func TestEmbeddingLifecycle_AllowedModels_FiltersPlatformChat(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-scoped-allowed"
	mockClient := newMockGatewayClient()
	restrictedAllowed := []string{DefaultEmbeddingModelName}
	lifecycle := NewEmbeddingLifecycleManager(mockClient, restrictedAllowed)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-scoped",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-scoped",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-scoped-key",
		Status:       "active",
	}
	db.Create(&instance)

	tm := rentity.TenantModel{
		ID:         "tm-scoped-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  DefaultEmbeddingModelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-scoped-key"] = &gateway.GenerateKeyRequest{
		Models: []string{DefaultEmbeddingModelName},
	}

	err := lifecycle.reconcileGatewayWhitelist(ctx, db, &instance, "", gatewayOverrideNone)
	if err != nil {
		t.Fatalf("reconcileGatewayWhitelist failed: %v", err)
	}

	mockClient.mu.Lock()
	storedKey := mockClient.generated["sk-scoped-key"]
	lastReq := mockClient.lastUpdateKeyReq
	mockClient.mu.Unlock()
	if storedKey == nil {
		t.Fatal("expected stored key in gateway, got nil")
	}
	if lastReq == nil {
		t.Fatal("expected update key request to be recorded, got nil")
	}
	if lastReq.Key != "sk-scoped-key" {
		t.Fatalf("expected update key %s, got %s", "sk-scoped-key", lastReq.Key)
	}
	if len(lastReq.Models) != 1 || lastReq.Models[0] != DefaultEmbeddingModelName {
		t.Fatalf("expected update key models [%s], got %v", DefaultEmbeddingModelName, lastReq.Models)
	}
	for _, m := range storedKey.Models {
		if m == "platform/chat" {
			t.Fatalf("platform/chat should not be added when allowedModels excludes it, got %v", storedKey.Models)
		}
	}
}

func TestEmbeddingLifecycle_Reactivate_DeletedInstance_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-react-del-inst"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-del-inst",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-del-inst",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "",
		Status:       "deleted",
	}
	db.Create(&instance)

	modelName := "embd-del-inst"
	tm := rentity.TenantModel{
		ID:         "tm-del-inst-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error reactivating on deleted instance, got nil")
	}

	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "disabled" {
		t.Fatalf("model status should remain disabled after rejection, got %s", verifyTM.Status)
	}
}

func TestEmbeddingLifecycle_Reactivate_DeniedByAllowlist_ReturnsError(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-react-denied"
	mockClient := newMockGatewayClient()
	restrictedAllowed := []string{DefaultEmbeddingModelName}
	lifecycle := NewEmbeddingLifecycleManager(mockClient, restrictedAllowed)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-react-denied",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-react-denied",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-react-denied-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "custom-denied-model"
	tm := rentity.TenantModel{
		ID:         "tm-react-denied-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error reactivating model not permitted by allowlist, got nil")
	}

	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "disabled" {
		t.Fatalf("model status should remain disabled, got %s", verifyTM.Status)
	}
}

func TestEmbeddingLifecycle_Retire_LastModel_SendsRevokedPlaceholder(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-revoked-placeholder"
	mockClient := newMockGatewayClient()
	restrictedAllowed := []string{DefaultEmbeddingModelName}
	lifecycle := NewEmbeddingLifecycleManager(mockClient, restrictedAllowed)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-revoked",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-revoked",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-revoked-key",
		Status:       "active",
	}
	db.Create(&instance)

	tm := rentity.TenantModel{
		ID:         "tm-revoked-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  DefaultEmbeddingModelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-revoked-key"] = &gateway.GenerateKeyRequest{
		Models: []string{DefaultEmbeddingModelName},
	}

	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, DefaultEmbeddingModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel failed: %v", err)
	}
	if res.State != EmbeddingStateTombstoned {
		t.Fatalf("expected state tombstoned, got %s", res.State)
	}

	mockClient.mu.Lock()
	lastReq := mockClient.lastUpdateKeyReq
	storedKey := mockClient.generated["sk-revoked-key"]
	mockClient.mu.Unlock()

	if lastReq == nil {
		t.Fatal("expected update key request to be sent, got nil")
	}
	if len(lastReq.Models) != 1 || lastReq.Models[0] != RevokedModelPlaceholder {
		t.Fatalf("expected update key models [%s], got %v", RevokedModelPlaceholder, lastReq.Models)
	}
	if len(storedKey.Models) != 1 || storedKey.Models[0] != RevokedModelPlaceholder {
		t.Fatalf("expected stored key models [%s], got %v", RevokedModelPlaceholder, storedKey.Models)
	}
}

func TestEmbeddingLifecycle_Reactivate_ConcurrentDecommission_AbortsAndCompensates(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-react-decom"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-react-decom",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-react-decom",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-react-decom-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-react-decom"
	tm := rentity.TenantModel{
		ID:         "tm-react-decom-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"is_managed":true,"deprecated":true,"tombstoned":true}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-react-decom-key"] = &gateway.GenerateKeyRequest{
		Models: []string{"platform/chat"},
	}

	lifecycle.onBeforeReactivateDB = func(tID, mName string) {
		// Concurrent decommission marks row as deleted
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Update("status", "deleted")
	}

	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error due to concurrent status change, got nil")
	}

	// Model must remain deleted
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "deleted" {
		t.Fatalf("expected status deleted, got %s", verifyTM.Status)
	}

	// Gateway key must not include modelName
	mockClient.mu.Lock()
	storedKey := mockClient.generated["sk-react-decom-key"]
	mockClient.mu.Unlock()
	for _, m := range storedKey.Models {
		if m == modelName {
			t.Fatalf("model %s should have been removed by compensation, got %v", modelName, storedKey.Models)
		}
	}
}

func TestEmbeddingLifecycle_Retire_ConcurrentDeletion_ReturnsTombstoned(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-retire-del"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-retire-del",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-retire-del",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-retire-del-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-retire-del"
	tm := rentity.TenantModel{
		ID:         "tm-retire-del-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	compositeID := modelName + "@" + PlatformInstanceName + "@" + ProviderOpenAIAPICompatible
	createTestKnowledgebase(t, db, "kb-retire-del", tenantID, compositeID, "", "1")

	lifecycle.onBeforeDeprecateDB = func(tID, mName string) {
		// Concurrent decommission marks row as deleted
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Update("status", "deleted")
	}

	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, modelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel returned error: %v", err)
	}
	if res.State != EmbeddingStateTombstoned {
		t.Fatalf("expected state tombstoned for concurrently deleted model, got %s", res.State)
	}
	if res.AllowedInGateway {
		t.Fatal("expected AllowedInGateway=false for deleted model")
	}
}

func TestEmbeddingLifecycle_Sweep_SkipsPreviouslyReactivatedModel(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-sweep-race"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-race",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-race",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-race-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-race"
	tm := rentity.TenantModel{
		ID:         "tm-race-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true,"read_only":true}`,
	}
	db.Create(&tm)

	// Reactivate model so it is no longer deprecated
	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, modelName)
	if err != nil {
		t.Fatalf("reactivate failed: %v", err)
	}

	// Now run sweep: candidate should be skipped because it was reactivated
	tombstoned, err := lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if tombstoned != 0 {
		t.Fatalf("expected 0 tombstoned models, got %d", tombstoned)
	}

	// Verify model remains active in DB
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "active" {
		t.Fatalf("expected model status 'active', got %q", verifyTM.Status)
	}
}

func TestEmbeddingLifecycle_Sweep_ConcurrentReactivation_EarlyAbort(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-early-abort"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-early-abort",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-early-abort",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-early-abort-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-early-abort"
	tm := rentity.TenantModel{
		ID:         "tm-early-abort-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true,"deprecated":true,"read_only":true}`,
	}
	db.Create(&tm)

	// Simulate concurrent reactivation occurring right before tombstoneModel is called
	lifecycle.onBeforeTombstone = func(tID, mName string) {
		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Updates(map[string]interface{}{
			"status":      "active",
			"extra":       `{"is_managed":true}`,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		})
	}

	tombstoned, err := lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("sweep returned unexpected error: %v", err)
	}
	if tombstoned != 0 {
		t.Fatalf("expected 0 tombstoned models due to early abort, got %d", tombstoned)
	}

	// Verify database state: must remain active with deprecated/tombstone flags cleared
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "active" {
		t.Fatalf("expected model status 'active', got %q", verifyTM.Status)
	}
	extra := parseExtraMap(verifyTM.Extra)
	if extra["tombstoned"] == true || extra["deprecated"] == true {
		t.Fatalf("model was overwritten by sweep: %+v", extra)
	}

	// Verify gateway was never touched because early abort prevented tombstone
	mockClient.mu.Lock()
	lastReq := mockClient.lastUpdateKeyReq
	mockClient.mu.Unlock()
	if lastReq != nil {
		t.Fatalf("expected no gateway update key request on early abort, got %v", lastReq)
	}
}

func TestEmbeddingLifecycle_Sweep_ConcurrentReactivation_OptimisticConflict(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-conflict"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-conflict",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-conflict",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-conflict-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-conflict"
	tm := rentity.TenantModel{
		ID:         "tm-conflict-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true,"deprecated":true,"read_only":true}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-conflict-key"] = &gateway.GenerateKeyRequest{
		Models: []string{"platform/chat", "platform/embedding", modelName},
	}

	// Simulate interleaving: right after gateway update in tombstoneModel, concurrent reactivation alters DB extra
	lifecycle.onBeforeTombstoneDB = func(tID, mName string) {
		// Verify that initial update from tombstoneModel removed modelName from stored key
		mockClient.mu.Lock()
		stored := mockClient.generated["sk-conflict-key"]
		mockClient.mu.Unlock()
		if stored == nil {
			t.Fatal("expected stored key in mockClient, got nil")
		}
		for _, m := range stored.Models {
			if m == modelName {
				t.Fatalf("expected initial update to remove %s, but found in %v", modelName, stored.Models)
			}
		}

		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Updates(map[string]interface{}{
			"status":      "active",
			"extra":       `{"is_managed":true}`,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		})
	}

	tombstoned, err := lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("sweep returned unexpected error: %v", err)
	}
	if tombstoned != 0 {
		t.Fatalf("expected 0 tombstoned models due to optimistic conflict, got %d", tombstoned)
	}

	// Verify database state: must remain active with deprecated/tombstone flags cleared
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "active" {
		t.Fatalf("expected model status 'active', got %q", verifyTM.Status)
	}
	extra := parseExtraMap(verifyTM.Extra)
	if extra["tombstoned"] == true || extra["deprecated"] == true {
		t.Fatalf("model was overwritten by sweep: %+v", extra)
	}

	// Verify gateway access: model must be preserved in virtual key whitelist via compensation
	mockClient.mu.Lock()
	storedKey := mockClient.generated["sk-conflict-key"]
	mockClient.mu.Unlock()
	if storedKey == nil {
		t.Fatal("expected stored key in mock gateway, got nil")
	}
	hasModel := false
	hasPlatformEmbedding := false
	for _, m := range storedKey.Models {
		if m == modelName {
			hasModel = true
		}
		if m == "platform/embedding" {
			hasPlatformEmbedding = true
		}
	}
	if !hasModel {
		t.Fatalf("expected model %s to be restored in stored key, got %v", modelName, storedKey.Models)
	}
	if !hasPlatformEmbedding {
		t.Fatalf("expected platform/embedding to be restored in stored key, got %v", storedKey.Models)
	}
}

func TestEmbeddingLifecycle_Tombstone_NilGatewayWithAPIKey_ReturnsError(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-nil-gw"
	lifecycle := NewEmbeddingLifecycleManager(nil) // nil gateway client

	provider := rentity.TenantModelProvider{
		ID:           "prov-nil-gw",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	if err := db.Create(&provider).Error; err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	instance := rentity.TenantModelInstance{
		ID:           "inst-nil-gw",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-has-key",
		Status:       "active",
	}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatalf("failed to create instance: %v", err)
	}

	modelName := "embd-no-gw"
	tm := rentity.TenantModel{
		ID:         "tm-nil-gw-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
	}
	if err := db.Create(&tm).Error; err != nil {
		t.Fatalf("failed to create tm: %v", err)
	}

	// Retire with 0 KBs should attempt to tombstone, but since APIKey is present and gateway is nil, return error
	_, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error when tombstoning with active API key and nil gateway client, got nil")
	}

	var reloadedTM rentity.TenantModel
	if err := db.First(&reloadedTM, "id = ?", tm.ID).Error; err != nil {
		t.Fatalf("failed to reload model after failed retirement: %v", err)
	}
	if reloadedTM.Status != "active" {
		t.Errorf("expected model status to remain 'active', got %q", reloadedTM.Status)
	}
	if reloadedTM.Extra != tm.Extra {
		t.Errorf("expected extra to remain %q, got %q", tm.Extra, reloadedTM.Extra)
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
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestEmbeddingLifecycle_Sweep_ConcurrentTombstone_AlreadyTombstonedWinning(t *testing.T) {
	ctx := context.Background()
	db := setupTestDB(t)
	tenantID := "tenant-concurrent-tomb"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)

	provider := rentity.TenantModelProvider{
		ID:           "prov-conc-tomb",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	if err := db.Create(&provider).Error; err != nil {
		t.Fatalf("failed to create provider: %v", err)
	}

	instance := rentity.TenantModelInstance{
		ID:           "inst-conc-tomb",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-conc-tomb-key",
		Status:       "active",
	}
	if err := db.Create(&instance).Error; err != nil {
		t.Fatalf("failed to create instance: %v", err)
	}

	modelName := "embd-conc-tomb"
	tm := rentity.TenantModel{
		ID:         "tm-conc-tomb-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true,"deprecated":true,"read_only":true}`,
	}
	if err := db.Create(&tm).Error; err != nil {
		t.Fatalf("failed to create tm: %v", err)
	}

	mockClient.generated["sk-conc-tomb-key"] = &gateway.GenerateKeyRequest{
		Models: []string{"platform/chat", modelName},
	}

	// Another sweep worker sets status = 'disabled' and extra = tombstoned right after gateway call
	lifecycle.onBeforeTombstoneDB = func(tID, mName string) {
		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Updates(map[string]interface{}{
			"status":      "disabled",
			"extra":       `{"is_managed":true,"deprecated":true,"tombstoned":true}`,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		})
	}

	tombstoned, err := lifecycle.SweepTombstoneCandidates(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("sweep returned unexpected error: %v", err)
	}
	// The current worker detected another worker already tombstoned the model
	if tombstoned != 0 {
		t.Fatalf("expected 0 additional tombstoned models, got %d", tombstoned)
	}

	// Model must remain disabled
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "disabled" {
		t.Fatalf("expected model status disabled, got %s", verifyTM.Status)
	}

	// Gateway must NOT restore modelName across any update
	mockClient.mu.Lock()
	var keyUpdates [][]string
	for _, req := range mockClient.updateHistory {
		if req.Key == "sk-conc-tomb-key" {
			keyUpdates = append(keyUpdates, req.Models)
		}
	}
	storedKey := mockClient.generated["sk-conc-tomb-key"]
	mockClient.mu.Unlock()

	if len(keyUpdates) == 0 {
		t.Fatal("expected at least one gateway update for key")
	}
	for i, models := range keyUpdates {
		for _, m := range models {
			if m == modelName {
				t.Fatalf("update %d restored model %s in gateway key, updates: %v", i, modelName, keyUpdates)
			}
		}
	}
	for _, m := range storedKey.Models {
		if m == modelName {
			t.Fatalf("model %s should remain excluded from gateway key, but was found", modelName)
		}
	}
}

func TestEmbeddingLifecycle_ParseExtraMap_PreservesNumbers(t *testing.T) {
	raw := `{"max_tokens":4096,"temperature":0.7,"nested":{"int64_val":9223372036854775807}}`
	m := parseExtraMap(raw)
	if m == nil {
		t.Fatal("expected non-nil map")
	}
	num, ok := m["max_tokens"].(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T (%v)", m["max_tokens"], m["max_tokens"])
	}
	if num.String() != "4096" {
		t.Fatalf("expected 4096, got %s", num.String())
	}
	nested, ok := m["nested"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected nested object, got %T", m["nested"])
	}
	big, ok := nested["int64_val"].(json.Number)
	if !ok {
		t.Fatalf("expected json.Number, got %T", nested["int64_val"])
	}
	if big.String() != "9223372036854775807" {
		t.Fatalf("expected 9223372036854775807, got %s", big.String())
	}
}

func TestEmbeddingLifecycle_Retire_ConcurrentModification_DeprecationAborted(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	tenantID := "tenant-retire-dep-race"
	createBareTestTenant(db, tenantID)
	_ = rec.Reconcile(ctx, db, tenantID)

	embdModelName := DefaultEmbeddingModelName
	compositeID := embdModelName + "@" + PlatformInstanceName + "@" + ProviderOpenAIAPICompatible

	// Create 1 active KB so retirement targets deprecation
	createTestKnowledgebase(t, db, "kb-dep-race", tenantID, compositeID, "", "1")

	var tm rentity.TenantModel
	db.Where("model_name = ?", embdModelName).First(&tm)

	// Inject competing write right after model is read but before DB update in Case A
	lifecycle.onBeforeDeprecateDB = func(tID, mName string) {
		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)
		if err := db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Updates(map[string]interface{}{
			"extra":       `{"is_managed":true,"competing_flag":"winner"}`,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}).Error; err != nil {
			t.Fatalf("competing write failed: %v", err)
		}
	}

	res, err := lifecycle.RetireEmbeddingModel(ctx, db, tenantID, embdModelName)
	if err != nil {
		t.Fatalf("RetireEmbeddingModel returned error: %v", err)
	}

	// Verify that retirement detected the collision and did not transition
	if res.Transitioned {
		t.Fatal("expected Transitioned=false on concurrent collision")
	}
	if res.State != EmbeddingStateActive {
		t.Fatalf("expected active state after competing update without deprecated flag, got %s", res.State)
	}

	// Verify the competing metadata was preserved in the database
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	extra := parseExtraMap(verifyTM.Extra)
	if extra["competing_flag"] != "winner" {
		t.Fatalf("expected competing_flag winner to be preserved, got %v", extra)
	}
}

func TestEmbeddingLifecycle_Reactivate_ConcurrentExtraModification_AbortsAndCompensates(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-react-extra"
	mockClient := newMockGatewayClient()
	lifecycle := NewEmbeddingLifecycleManager(mockClient)
	ctx := context.Background()

	provider := rentity.TenantModelProvider{
		ID:           "prov-react-extra",
		TenantID:     tenantID,
		ProviderName: ProviderOpenAIAPICompatible,
	}
	db.Create(&provider)

	instance := rentity.TenantModelInstance{
		ID:           "inst-react-extra",
		ProviderID:   provider.ID,
		InstanceName: PlatformInstanceName,
		APIKey:       "sk-react-extra-key",
		Status:       "active",
	}
	db.Create(&instance)

	modelName := "embd-react-extra"
	tm := rentity.TenantModel{
		ID:         "tm-react-extra-1",
		ProviderID: provider.ID,
		InstanceID: instance.ID,
		ModelName:  modelName,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "disabled",
		Extra:      `{"is_managed":true,"deprecated":true,"tombstoned":true,"custom_token":"val_123"}`,
	}
	db.Create(&tm)

	mockClient.generated["sk-react-extra-key"] = &gateway.GenerateKeyRequest{
		Models: []string{"platform/chat"},
	}

	lifecycle.onBeforeReactivateDB = func(tID, mName string) {
		// Concurrent writer edits extra metadata before reactivation DB update
		db.Model(&rentity.TenantModel{}).Where("id = ?", tm.ID).Update("extra", `{"is_managed":true,"deprecated":true,"tombstoned":true,"custom_token":"val_456"}`)
	}

	err := lifecycle.ReactivateEmbeddingModel(ctx, db, tenantID, modelName)
	if err == nil {
		t.Fatal("expected error due to concurrent extra modification, got nil")
	}

	// Compensation must have rolled back the gateway key
	mockClient.mu.Lock()
	storedKey := mockClient.generated["sk-react-extra-key"]
	mockClient.mu.Unlock()
	if len(storedKey.Models) != 1 || storedKey.Models[0] != "platform/chat" {
		t.Fatalf("expected gateway models to be [platform/chat] after compensation, got %v", storedKey.Models)
	}

	// Model must remain disabled and preserve the concurrent metadata change
	var verifyTM rentity.TenantModel
	db.First(&verifyTM, "id = ?", tm.ID)
	if verifyTM.Status != "disabled" {
		t.Fatalf("model status should remain disabled, got %s", verifyTM.Status)
	}
	extra := parseExtraMap(verifyTM.Extra)
	if extra["custom_token"] != "val_456" {
		t.Fatalf("expected custom_token val_456 to be preserved, got %v", extra)
	}
}
