package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"
	"ragflow/extensions/model-platform/backend/testenv"
	rentity "ragflow/internal/entity"
	"ragflow/internal/permission"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type mockGatewayClient struct {
	mu               sync.Mutex
	generated        map[string]*gateway.GenerateKeyRequest
	deletedKeys      []string
	lastUpdateKeyReq *gateway.UpdateKeyRequest
	updateHistory    []*gateway.UpdateKeyRequest
	generateErr      error
	updateErr        error
	deleteErr        error
	listErr          error
	onGenerate       func()
	keyCounter       int32
}

func newMockGatewayClient() *mockGatewayClient {
	return &mockGatewayClient{
		generated: make(map[string]*gateway.GenerateKeyRequest),
	}
}

func (m *mockGatewayClient) GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.onGenerate != nil {
		m.onGenerate()
	}

	if m.generateErr != nil {
		return nil, m.generateErr
	}

	idx := atomic.AddInt32(&m.keyCounter, 1)
	key := fmt.Sprintf("sk-litellm-vk-%s-%d", req.TeamID, idx)
	m.generated[key] = req

	return &gateway.GenerateKeyResponse{
		Key:       key,
		KeyName:   req.KeyName,
		Models:    req.Models,
		Expires:   req.Duration,
		MaxBudget: req.MaxBudget,
	}, nil
}

func (m *mockGatewayClient) UpdateKey(ctx context.Context, req *gateway.UpdateKeyRequest) (*gateway.UpdateKeyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastUpdateKeyReq = req
	m.updateHistory = append(m.updateHistory, req)
	if m.updateErr != nil {
		return nil, m.updateErr
	}

	if gen, ok := m.generated[req.Key]; ok {
		if len(req.Models) > 0 {
			gen.Models = req.Models
		}
	}

	return &gateway.UpdateKeyResponse{
		Key:    req.Key,
		Models: req.Models,
	}, nil
}

func (m *mockGatewayClient) ListTeamKeys(ctx context.Context, teamID string) ([]gateway.TeamKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.listErr != nil {
		return nil, m.listErr
	}

	var keys []gateway.TeamKey
	for key, req := range m.generated {
		if req.TeamID == teamID {
			keys = append(keys, gateway.TeamKey{Token: gateway.HashKey(key), KeyAlias: req.KeyName, TeamID: req.TeamID})
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Token < keys[j].Token })
	return keys, nil
}

// DeleteKeysByHash mirrors the real client: keys that are already gone are not an error.
func (m *mockGatewayClient) DeleteKeysByHash(ctx context.Context, hashes []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deleteErr != nil {
		return m.deleteErr
	}

	for _, h := range hashes {
		for key := range m.generated {
			if gateway.HashKey(key) == h {
				delete(m.generated, key)
				m.deletedKeys = append(m.deletedKeys, h)
			}
		}
	}
	return nil
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:reconcile_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	// Migrate reconcile schema
	if err := migration.MigrateReconcileSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}

	// Migrate preference schema
	if err := migration.MigratePreferenceSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate preference schema: %v", err)
	}

	// Migrate upstream entity tables
	err = db.AutoMigrate(
		&rentity.Tenant{},
		&rentity.UserTenant{},
		&rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{},
		&rentity.TenantModel{},
		&rentity.Knowledgebase{},
	)
	if err != nil {
		t.Fatalf("failed to migrate tenant model tables: %v", err)
	}

	// The baseline catalog replaces the models Reconcile used to hardcode
	if err := migration.MigratePublishSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := testenv.SeedPlatformTestModels(db); err != nil {
		t.Fatalf("failed to seed the baseline catalog: %v", err)
	}

	return db
}

func TestTenantReconciler_SuccessfulProvisioning(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-test-1"

	err := rec.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// 1. Verify TenantModelProvider (must populate UserID for upstream permission checks)
	var provider rentity.TenantModelProvider
	err = db.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).First(&provider).Error
	if err != nil {
		t.Fatalf("expected TenantModelProvider to exist: %v", err)
	}
	if provider.UserID != tenantID {
		t.Errorf("expected provider.UserID to fall back to tenantID %q, got %q", tenantID, provider.UserID)
	}

	// 2. Verify TenantModelInstance
	var instance rentity.TenantModelInstance
	err = db.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error
	if err != nil {
		t.Fatalf("expected TenantModelInstance to exist: %v", err)
	}
	if !strings.HasPrefix(instance.APIKey, "sk-litellm-vk-") {
		t.Errorf("expected virtual key, got %q", instance.APIKey)
	}
	var extraStrings map[string]string
	if err := json.Unmarshal([]byte(instance.Extra), &extraStrings); err != nil {
		t.Fatalf("instance.Extra must unmarshal as map[string]string for upstream compatibility: %v (raw=%s)", err, instance.Extra)
	}
	if extraStrings["is_managed"] != "true" {
		t.Errorf("expected extra is_managed=\"true\", got %q", extraStrings["is_managed"])
	}
	if extraStrings["base_url"] != "http://127.0.0.1:4000/v1" {
		t.Errorf("expected normalized base_url \"http://127.0.0.1:4000/v1\", got %q", extraStrings["base_url"])
	}

	// 3. Verify TenantModel records
	var models []rentity.TenantModel
	err = db.Where("instance_id = ?", instance.ID).Find(&models).Error
	if err != nil {
		t.Fatalf("failed to find tenant models: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models (chat and embedding), got %d", len(models))
	}

	chatFound := false
	embdFound := false
	for _, m := range models {
		if m.ModelName == DefaultChatModelName && m.ModelType == int(rentity.ModelTypeChat) {
			chatFound = true
		}
		if m.ModelName == DefaultEmbeddingModelName && m.ModelType == int(rentity.ModelTypeEmbedding) {
			embdFound = true
		}
	}
	if !chatFound || !embdFound {
		t.Errorf("expected both chat and embedding models, got %v", models)
	}

	// 4. Verify Reconcile state
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to get reconcile state: %v", err)
	}
	if state.Status != entity.StatusReady {
		t.Errorf("expected status ready, got %s", state.Status)
	}
	if !strings.HasPrefix(state.GatewayKeyAlias, "tenant_tenant-test-1_") {
		t.Errorf("expected a key alias labelled with the tenant, got %s", state.GatewayKeyAlias)
	}
	if state.KeyHash != gateway.HashKey(instance.APIKey) {
		t.Errorf("expected key hash %s, got %s", gateway.HashKey(instance.APIKey), state.KeyHash)
	}
}

func TestTenantReconciler_Idempotency(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-idempotent-1"

	// Reconcile 10 times consecutively
	for i := 0; i < 10; i++ {
		if err := rec.Reconcile(ctx, db, tenantID); err != nil {
			t.Fatalf("Reconcile iteration %d failed: %v", i, err)
		}
	}

	// Assert exactly 1 provider, 1 instance, and 2 models exist
	var providerCount, instanceCount, modelCount int64
	db.Model(&rentity.TenantModelProvider{}).Where("tenant_id = ?", tenantID).Count(&providerCount)
	db.Model(&rentity.TenantModelInstance{}).Where("instance_name = ?", PlatformInstanceName).Count(&instanceCount)
	db.Model(&rentity.TenantModel{}).Count(&modelCount)

	if providerCount != 1 {
		t.Errorf("expected exactly 1 provider, got %d", providerCount)
	}
	if instanceCount != 1 {
		t.Errorf("expected exactly 1 instance, got %d", instanceCount)
	}
	if modelCount != 2 {
		t.Errorf("expected exactly 2 models, got %d", modelCount)
	}

	// Assert exactly 1 key generated across all 10 reconciliations
	mockClient.mu.Lock()
	keysCount := len(mockClient.generated)
	mockClient.mu.Unlock()
	if keysCount != 1 {
		t.Errorf("expected exactly 1 generated key, got %d", keysCount)
	}
}

func TestTenantReconciler_CompensatingRollback(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-rollback-1"

	// Drop tenant_model table to force a database failure during transaction
	db.Migrator().DropTable(&rentity.TenantModel{})

	err := rec.Reconcile(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected reconcile to fail due to dropped table, got nil")
	}

	// Verify compensating rollback revoked the virtual key
	mockClient.mu.Lock()
	deletedCount := len(mockClient.deletedKeys)
	mockClient.mu.Unlock()

	if deletedCount != 1 {
		t.Errorf("expected 1 key revoked via compensating rollback, got %d", deletedCount)
	}

	// Verify state record is marked degraded
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status degraded after rollback, got %s", state.Status)
	}
	if state.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", state.RetryCount)
	}
}

func TestTenantReconciler_GatewayError(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	mockClient.generateErr = errors.New("gateway connection timeout (504)")

	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)
	ctx := context.Background()
	tenantID := "tenant-gateway-err-1"

	err := rec.Reconcile(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected reconcile to fail when gateway fails, got nil")
	}

	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("failed to get state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status degraded, got %s", state.Status)
	}
}

func TestTenantReconciler_ConcurrentReconciliations(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-concurrent-1"

	const concurrency = 5
	var wg sync.WaitGroup
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = rec.Reconcile(ctx, db, tenantID)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent worker %d failed: %v", i, err)
		}
	}

	// Verify exactly 1 provider and 1 instance
	var providerCount, instanceCount int64
	db.Model(&rentity.TenantModelProvider{}).Where("tenant_id = ?", tenantID).Count(&providerCount)
	db.Model(&rentity.TenantModelInstance{}).Where("instance_name = ?", PlatformInstanceName).Count(&instanceCount)

	if providerCount != 1 {
		t.Errorf("expected 1 provider, got %d", providerCount)
	}
	if instanceCount != 1 {
		t.Errorf("expected 1 instance, got %d", instanceCount)
	}
}

func TestTenantReconciler_AllowedModelsFilter(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	// Only allow chat model, omitting embedding model
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", []string{DefaultChatModelName})

	ctx := context.Background()
	tenantID := "tenant-model-scope-1"

	err := rec.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var models []rentity.TenantModel
	db.Where("provider_id IN (SELECT id FROM tenant_model_provider WHERE tenant_id = ?)", tenantID).Find(&models)
	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	if models[0].ModelName != DefaultChatModelName {
		t.Errorf("expected %s, got %s", DefaultChatModelName, models[0].ModelName)
	}
}

func TestTenantReconciler_ContextTimeout_MarksDegraded(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()

	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockClient.onGenerate = func() {
		cancel() // Cancel caller context during gateway RPC to simulate timeout
	}
	mockClient.generateErr = errors.New("gateway RPC timed out")

	tenantID := "tenant-ctx-timeout-1"
	err := rec.Reconcile(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected error with cancelled context, got nil")
	}

	// Verify that MarkDegraded succeeded using recoveryCtx and record is degraded, not stuck in provisioning
	rdao := dao.NewReconcileDAO()
	state, err := rdao.GetByTenantID(context.Background(), db, tenantID)
	if err != nil {
		t.Fatalf("failed to fetch state: %v", err)
	}
	if state.Status != entity.StatusDegraded {
		t.Errorf("expected status degraded after timeout, got %s", state.Status)
	}
}

func TestTenantReconciler_OptimisticLockConflict_DeleteKeyFailureReturnsError(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	mockClient.deleteErr = errors.New("gateway unreachable on delete")
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-conflict-delerr"

	initialRec := entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	}
	db.Create(&initialRec)

	mockClient.onGenerate = func() {
		_ = db.Model(&entity.TenantReconcileRecord{}).
			Where("tenant_id = ?", tenantID).
			Updates(map[string]interface{}{
				"version": initialRec.Version + 10,
				"status":  entity.StatusReady,
			}).Error
	}

	err := rec.Reconcile(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected error when DeleteKey fails on conflict, got nil")
	}
	if !strings.Contains(err.Error(), "compensating rollback incomplete") {
		t.Errorf("expected error to mention 'compensating rollback incomplete', got: %v", err)
	}
}

func TestTenantReconciler_OptimisticLockConflict_DeleteKeySuccessReturnsNil(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-conflict-ok"

	initialRec := entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	}
	db.Create(&initialRec)

	mockClient.onGenerate = func() {
		_ = db.Model(&entity.TenantReconcileRecord{}).
			Where("tenant_id = ?", tenantID).
			Updates(map[string]interface{}{
				"version": initialRec.Version + 10,
				"status":  entity.StatusReady,
			}).Error
	}

	err := rec.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("expected nil on successful compensation during conflict, got: %v", err)
	}
}

func TestTenantReconciler_UpstreamPermissionAndOwnerWiring(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	// Verify already-versioned base_url is not double-suffixed with /v1/v1.
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000/v1/", nil)

	ctx := context.Background()
	tenantID := "tenant-perm-1"
	ownerID := "owner-user-1"
	statusValid := "1"
	if err := db.Create(&rentity.Tenant{ID: tenantID, Status: &statusValid}).Error; err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if err := db.Create(&rentity.UserTenant{
		ID:       "ut-owner-1",
		UserID:   ownerID,
		TenantID: tenantID,
		Role:     "owner",
		Status:   &statusValid,
	}).Error; err != nil {
		t.Fatalf("seed owner membership: %v", err)
	}

	if err := rec.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible).First(&provider).Error; err != nil {
		t.Fatalf("load provider: %v", err)
	}
	if provider.UserID != ownerID {
		t.Fatalf("provider.UserID = %q, want owner %q", provider.UserID, ownerID)
	}

	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, PlatformInstanceName).First(&instance).Error; err != nil {
		t.Fatalf("load instance: %v", err)
	}
	var extra map[string]string
	if err := json.Unmarshal([]byte(instance.Extra), &extra); err != nil {
		t.Fatalf("instance.Extra failed map[string]string unmarshal: %v", err)
	}
	if extra["base_url"] != "http://127.0.0.1:4000/v1" {
		t.Fatalf("base_url = %q, want http://127.0.0.1:4000/v1", extra["base_url"])
	}

	var model rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ?", instance.ID, DefaultChatModelName).First(&model).Error; err != nil {
		t.Fatalf("load model: %v", err)
	}

	// Verify real upstream permission.NewDatabaseChecker authorizes the owner on provider, instance, and model.
	permCtx := permission.WithIdentity(ctx, ownerID, tenantID)
	checker := permission.NewDatabaseChecker(db)
	if err := checker.CheckResource(permCtx, permission.ResourceRef{Kind: permission.ResourceKindModelProvider, ID: provider.ID}, permission.OperationRead); err != nil {
		t.Fatalf("upstream permission check for provider failed: %v", err)
	}
	if err := checker.CheckResource(permCtx, permission.ResourceRef{Kind: permission.ResourceKindModelInstance, ID: instance.ID}, permission.OperationUse); err != nil {
		t.Fatalf("upstream permission check for instance failed: %v", err)
	}
	if err := checker.CheckResource(permCtx, permission.ResourceRef{Kind: permission.ResourceKindModel, ID: model.ID}, permission.OperationUse); err != nil {
		t.Fatalf("upstream permission check for model failed: %v", err)
	}
}
