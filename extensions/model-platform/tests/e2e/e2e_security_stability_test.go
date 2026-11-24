package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mpdao "ragflow/extensions/model-platform/backend/dao"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/guard"
	mpmetering "ragflow/extensions/model-platform/backend/metering"
	mpmigration "ragflow/extensions/model-platform/backend/migration"
	mpreconciler "ragflow/extensions/model-platform/backend/reconciler"
	"ragflow/extensions/model-platform/backend/testenv"
	"ragflow/internal/common"
	rdao "ragflow/internal/dao"
	rentity "ragflow/internal/entity"
	"ragflow/internal/entity/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupStabilityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:stability_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	ctx := context.Background()
	_ = db.AutoMigrate(
		&rentity.Tenant{},
		&rentity.User{},
		&rentity.UserTenant{},
		&rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{},
		&rentity.TenantModel{},
		&rentity.Knowledgebase{},
		&rentity.Document{},
	)
	_ = mpmigration.MigrateReconcileSchema(ctx, db)
	_ = mpmigration.MigratePreferenceSchema(ctx, db)
	_ = mpmigration.MigrateSpendLogSchema(ctx, db)
	_ = mpmigration.MigrateUsageSchema(ctx, db)
	_ = mpmigration.MigratePublishSchema(ctx, db)
	if err := testenv.SeedPlatformTestModels(db); err != nil {
		t.Fatalf("failed to seed the baseline catalog: %v", err)
	}

	return db
}

// 1. Managed Provider & Instance Anti-Deletion Security Test
func TestSecurityStability_ManagedProviderAntiDeletion(t *testing.T) {
	db := setupStabilityTestDB(t)
	oldDB := rdao.DB
	rdao.DB = db
	defer func() { rdao.DB = oldDB }()

	tenantID := "tenant-anti-delete"
	statusValid := "1"
	tName := "Anti Delete Tenant"
	db.Create(&rentity.Tenant{ID: tenantID, Name: &tName, Status: &statusValid})

	// Seed platform managed provider and instance
	prov := rentity.TenantModelProvider{
		ID:           "prov-managed-1",
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	}
	db.Create(&prov)

	inst := rentity.TenantModelInstance{
		ID:           "inst-managed-1",
		ProviderID:   prov.ID,
		InstanceName: "platform",
		APIKey:       "sk-litellm-protected",
	}
	db.Create(&inst)

	// Seed unmanaged custom provider
	customProv := rentity.TenantModelProvider{
		ID:           "prov-custom-1",
		TenantID:     tenantID,
		ProviderName: "Ollama",
	}
	db.Create(&customProv)

	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Inject tenant context middleware mimicking AuthMiddleware
	r.Use(func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Next()
	})
	r.Use(guard.ManagedProviderGuardMiddleware())

	okHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "deleted"})
	}

	r.DELETE("/api/v1/providers/:provider_id_or_name", okHandler)
	r.DELETE("/api/v1/providers/:provider_id_or_name/instances/:instance_id_or_name", okHandler)

	// Case 1: Tenant attempts to DELETE platform managed provider -> MUST BE BLOCKED (403)
	req1, _ := http.NewRequest(http.MethodDelete, "/api/v1/providers/OpenAI-API-Compatible", nil)
	resp1 := httptest.NewRecorder()
	r.ServeHTTP(resp1, req1)

	if resp1.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when deleting managed provider, got %d", resp1.Code)
	}

	// Case 2: Tenant attempts to DELETE platform instance -> MUST BE BLOCKED (403)
	req2, _ := http.NewRequest(http.MethodDelete, "/api/v1/providers/OpenAI-API-Compatible/instances/platform", nil)
	resp2 := httptest.NewRecorder()
	r.ServeHTTP(resp2, req2)

	if resp2.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when deleting managed instance, got %d", resp2.Code)
	}

	// Case 3: Tenant deletes custom unmanaged provider -> MUST BE ALLOWED (200)
	req3, _ := http.NewRequest(http.MethodDelete, "/api/v1/providers/Ollama", nil)
	resp3 := httptest.NewRecorder()
	r.ServeHTTP(resp3, req3)

	if resp3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when deleting custom provider, got %d", resp3.Code)
	}

	// Assert DB records remain intact
	var checkProv rentity.TenantModelProvider
	if err := db.Where("id = ?", prov.ID).First(&checkProv).Error; err != nil {
		t.Fatalf("managed provider was deleted: %v", err)
	}
	var checkInst rentity.TenantModelInstance
	if err := db.Where("id = ?", inst.ID).First(&checkInst).Error; err != nil {
		t.Fatalf("managed instance was deleted: %v", err)
	}
}

// 2. Gateway Outage Simulation & Tier-2 Poller Self-Healing Test
type outageMockGateway struct {
	mu        sync.Mutex
	isHealthy bool
	callCount int
}

func (m *outageMockGateway) GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if !m.isHealthy {
		return nil, errors.New("503 Service Unavailable: gateway cluster unreachable")
	}
	return &gateway.GenerateKeyResponse{
		Key:     "sk-litellm-healed-" + req.TeamID,
		KeyName: req.KeyName,
	}, nil
}

func (m *outageMockGateway) UpdateKey(ctx context.Context, req *gateway.UpdateKeyRequest) (*gateway.UpdateKeyResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.isHealthy {
		return nil, errors.New("503 Service Unavailable")
	}
	return &gateway.UpdateKeyResponse{Key: req.Key}, nil
}

func (m *outageMockGateway) DeleteKeysByHash(ctx context.Context, hashes []string) error {
	return nil
}

func (m *outageMockGateway) ListTeamKeys(ctx context.Context, teamID string) ([]gateway.TeamKey, error) {
	return nil, nil
}

func TestSecurityStability_GatewayOutageAndSelfHealing(t *testing.T) {
	db := setupStabilityTestDB(t)
	ctx := context.Background()
	tenantID := "tenant-outage-heal"
	statusValid := "1"
	tName := "Outage Tenant"
	db.Create(&rentity.Tenant{ID: tenantID, Name: &tName, Status: &statusValid})

	mockGW := &outageMockGateway{isHealthy: false} // Gateway is DOWN initially

	reconciler := mpreconciler.NewTenantReconciler(
		mockGW,
		"http://127.0.0.1:4000",
		[]string{"gpt-4o-mini", "text-embedding-3-small"},
	)

	// Seed tenant reconcile record as pending
	reconcileDAO := mpdao.NewReconcileDAO()
	db.Create(&mpentity.TenantReconcileRecord{
		TenantID: tenantID,
		Status:   mpentity.StatusPending,
		Version:  1,
	})

	// 1. Initial reconciliation during gateway outage
	err := reconciler.Reconcile(ctx, db, tenantID)
	if err == nil {
		t.Fatal("expected reconcile error during gateway outage, got nil")
	}

	// Verify record transitioned to DEGRADED
	rec, err := reconcileDAO.GetByTenantID(ctx, db, tenantID)
	if err != nil || rec.Status != mpentity.StatusDegraded {
		t.Fatalf("expected status degraded after outage, got: %+v, err: %v", rec, err)
	}

	// 2. Gateway recovers
	mockGW.mu.Lock()
	mockGW.isHealthy = true
	mockGW.mu.Unlock()

	// Update next_retry_at to past so record is immediately due for tier-2 polling
	db.Model(&mpentity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Update("next_retry_at", time.Now().Add(-1*time.Minute))

	// Initialize SelfHealingController (poller) and trigger sweep
	bus := event.NewEventBus(10)
	defer bus.Stop()
	poller := mpreconciler.NewSelfHealingController(db, bus, reconciler)

	// Execute tier-2 sweep
	poller.PollAndHeal(ctx)
	if poller.Tier2HealedCount() != 1 {
		t.Fatalf("expected 1 tenant healed by poller, got %d", poller.Tier2HealedCount())
	}

	// Verify record transitioned to READY
	healedRec, err := reconcileDAO.GetByTenantID(ctx, db, tenantID)
	if err != nil || healedRec.Status != mpentity.StatusReady {
		t.Fatalf("expected status ready after self-healing, got: %+v, err: %v", healedRec, err)
	}
	if healedRec.KeyHash == "" || healedRec.LastError != "" {
		t.Fatalf("expected valid key hash and cleared error, got: %+v", healedRec)
	}
}

// 3. Vector Model Deprecation & Read-Only Search Test
func TestSecurityStability_EmbeddingDeprecationReadOnlySearch(t *testing.T) {
	db := setupStabilityTestDB(t)
	ctx := context.Background()
	tenantID := "tenant-lifecycle-search"
	statusValid := "1"
	tName := "Lifecycle Tenant"
	db.Create(&rentity.Tenant{ID: tenantID, Name: &tName, Status: &statusValid})

	// Setup mock gateway that answers embeddings and key updates
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			vector := make([]float64, 1536)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"object": "list",
				"model":  "text-embedding-3-small",
				"data":   []map[string]interface{}{{"object": "embedding", "index": 0, "embedding": vector}},
			})
			return
		}
		if r.URL.Path == "/key/update" {
			json.NewEncoder(w).Encode(gateway.UpdateKeyResponse{Key: "sk-active-key"})
			return
		}
	}))
	defer server.Close()

	// Seed managed provider, instance, and tenant model
	prov := rentity.TenantModelProvider{
		ID:           "prov-managed-lifecycle",
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	}
	db.Create(&prov)

	inst := rentity.TenantModelInstance{
		ID:           "inst-managed-lifecycle",
		ProviderID:   prov.ID,
		InstanceName: "platform",
		APIKey:       "sk-active-key",
	}
	db.Create(&inst)

	tm := rentity.TenantModel{
		ID:         "tm-lifecycle-embd",
		ProviderID: prov.ID,
		InstanceID: inst.ID,
		ModelName:  "text-embedding-3-small",
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"is_managed":true}`,
	}
	db.Create(&tm)

	// Seed Knowledgebase bound to text-embedding-3-small
	kbID := "kb-lifecycle-1"
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		TenantID: tenantID,
		Name:     "Active Search KB",
		EmbdID:   "text-embedding-3-small",
		DocNum:   5,
		ChunkNum: 50,
		Status:   &statusValid,
	})

	gwClient := gateway.NewClient(gateway.GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: "sk-master",
		Timeout:   5 * time.Second,
	})
	lifecycleMgr := mpreconciler.NewEmbeddingLifecycleManager(gwClient)

	// Retire model while KB is active -> enters Deprecated/Read-Only state
	retireRes, err := lifecycleMgr.RetireEmbeddingModel(ctx, db, tenantID, "text-embedding-3-small")
	if err != nil {
		t.Fatalf("retire model failed: %v", err)
	}

	if retireRes.State != mpreconciler.EmbeddingStateDeprecated {
		t.Fatalf("expected state deprecated for active KB, got %s", retireRes.State)
	}
	if !retireRes.AllowedInGateway {
		t.Fatal("expected model to remain allowed in gateway during deprecation")
	}

	// Verify creating a new dataset with the deprecated embedding model is blocked by the guard
	gin.SetMode(gin.TestMode)
	createRouter := gin.New()
	createRouter.Use(func(c *gin.Context) {
		c.Set("tenant_id", tenantID)
		c.Next()
	})
	createRouter.Use(guard.DatasetImmutabilityGuardMiddleware(db))
	createRouter.POST("/api/v1/datasets", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0})
	})

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/datasets", strings.NewReader(`{"name":"New KB","embedding_model":"text-embedding-3-small"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createW := httptest.NewRecorder()
	createRouter.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 when creating KB with deprecated model, got %d (body: %s)", createW.Code, createW.Body.String())
	}

	// Verify existing Knowledgebase can STILL perform vector search / embedding calls
	driver := models.NewOpenAIAPICompatibleModel(map[string]string{}, models.URLSuffix{Embedding: "embeddings"})
	apiKey := "sk-active-key"
	apiCfg := &models.APIConfig{ApiKey: &apiKey, BaseURL: &server.URL}

	embModel := "text-embedding-3-small"
	var usage common.ModelUsage
	vecs, err := driver.Embed(ctx, &embModel, models.EmbedRequest{Texts: []string{"search query"}}, apiCfg, nil, &usage)
	if err != nil {
		t.Fatalf("deprecated model vector search failed: %v", err)
	}
	if len(vecs) != 1 || len(vecs[0].Embedding) != 1536 {
		t.Fatalf("expected valid 1536-dim embedding vector during deprecation, got len: %d", len(vecs))
	}
}

// 4. Dataset Vector Space Immutability Test
func TestSecurityStability_DatasetVectorSpaceImmutability(t *testing.T) {
	db := setupStabilityTestDB(t)
	tenantID := "tenant-immut"
	statusValid := "1"

	kbID := "kb-immut-parsed"
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		TenantID: tenantID,
		Name:     "Immutable KB",
		EmbdID:   "text-embedding-3-small",
		DocNum:   2,
		ChunkNum: 20,
		Status:   &statusValid,
	})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(guard.DatasetImmutabilityGuardMiddleware(db))

	okHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
	}
	r.PUT("/api/v1/datasets/:dataset_id", okHandler)

	// Case 1: Tamper embedding model on parsed dataset -> BLOCKED (400)
	tamperBody, _ := json.Marshal(map[string]interface{}{
		"embd_id": "different-vector-space-model",
	})
	req1, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(tamperBody))
	req1.Header.Set("Content-Type", "application/json")
	resp1 := httptest.NewRecorder()
	r.ServeHTTP(resp1, req1)

	if resp1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when altering embedding model, got %d", resp1.Code)
	}

	// Case 2: Update dataset title/description on parsed dataset -> ALLOWED (200)
	metaBody, _ := json.Marshal(map[string]interface{}{
		"name":        "Renamed Immutable KB",
		"description": "Updated description without changing vectors",
	})
	req2, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(metaBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2 := httptest.NewRecorder()
	r.ServeHTTP(resp2, req2)

	if resp2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for non-embedding metadata update, got %d", resp2.Code)
	}

	// Case 3: Create KB with retired/deprecated model -> BLOCKED (400)
	createHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	}
	r.POST("/api/v1/datasets", createHandler)

	prov := rentity.TenantModelProvider{
		ID:           "prov-immut-1",
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	}
	db.Create(&prov)
	db.Create(&rentity.TenantModel{
		ID:         "tm-immut-dep",
		ProviderID: prov.ID,
		ModelName:  "deprecated-vector-model",
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true}`,
	})

	createBody, _ := json.Marshal(map[string]interface{}{
		"name":            "Forbidden Deprecated KB",
		"embedding_model": "deprecated-vector-model",
	})
	req3, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(createBody))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-Tenant-ID", tenantID)
	resp3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(resp3)
	c3.Request = req3
	c3.Set("tenant_id", tenantID)
	r.HandleContext(c3)

	if resp3.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when creating dataset with deprecated model, got %d", resp3.Code)
	}

	// Case 4: Mutate empty KB to deprecated model -> BLOCKED (400)
	emptyKBID := "kb-immut-empty"
	db.Create(&rentity.Knowledgebase{
		ID:       emptyKBID,
		TenantID: tenantID,
		Name:     "Empty KB Attempting Deprecated",
		EmbdID:   "text-embedding-3-large",
		DocNum:   0,
		ChunkNum: 0,
		Status:   &statusValid,
	})
	switchBody, _ := json.Marshal(map[string]interface{}{
		"embedding_model": "deprecated-vector-model",
	})
	req4, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+emptyKBID, bytes.NewBuffer(switchBody))
	req4.Header.Set("Content-Type", "application/json")
	resp4 := httptest.NewRecorder()
	c4, _ := gin.CreateTestContext(resp4)
	c4.Request = req4
	c4.Set("tenant_id", tenantID)
	r.HandleContext(c4)

	if resp4.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when mutating empty dataset to deprecated model, got %d", resp4.Code)
	}
}

// 5. Billing Idempotency & 48h Window Replay Test
type mockSpendLogClient struct {
	entries []gateway.SpendLogEntry
	err     error
}

func (m *mockSpendLogClient) GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error) {
	if m.err != nil {
		return nil, m.err
	}
	if params.Page > 0 && params.PageSize > 0 {
		start := (params.Page - 1) * params.PageSize
		if start >= len(m.entries) {
			return []gateway.SpendLogEntry{}, nil
		}
		end := start + params.PageSize
		if end > len(m.entries) {
			end = len(m.entries)
		}
		return m.entries[start:end], nil
	}
	return m.entries, nil
}

func TestSecurityStability_BillingIdempotencyAndReplay(t *testing.T) {
	db := setupStabilityTestDB(t)
	ctx := context.Background()
	tenantID := "tenant-replay-test"

	now := time.Now().UTC()

	// Seed 100 unique raw spend logs over the last 48 hours
	var spendEntries []gateway.SpendLogEntry
	for i := 1; i <= 100; i++ {
		spendEntries = append(spendEntries, gateway.SpendLogEntry{
			SpendID:          fmt.Sprintf("spend-replay-%03d", i),
			Model:            "gpt-4o",
			TeamID:           tenantID,
			CallType:         "chat",
			TotalTokens:      100,
			PromptTokens:     70,
			CompletionTokens: 30,
			Spend:            0.0015,
			StartTime:        now.Add(-time.Duration(i%40) * time.Hour).Format("2006-01-02 15:04:05"),
		})
	}

	mockClient := &mockSpendLogClient{entries: spendEntries}
	collector := mpmetering.NewSpendLogCollector(db, mockClient)
	pipeline := mpmetering.NewUsagePipeline(db)

	start := now.Add(-48 * time.Hour)
	end := now

	// Execute Collector and Pipeline 5 consecutive times (simulating redundant replays and backfills)
	for round := 1; round <= 5; round++ {
		cRes, err := collector.CollectSpendLogs(ctx, start, end)
		if err != nil {
			t.Fatalf("round %d collector failed: %v", round, err)
		}
		if round > 1 && cRes.InsertedCount != 0 {
			t.Fatalf("round %d inserted %d duplicate logs (expected 0)", round, cRes.InsertedCount)
		}

		pRes, err := pipeline.RunWindow(ctx, 48)
		if err != nil {
			t.Fatalf("round %d pipeline failed: %v", round, err)
		}
		if pRes.AggregatedRecords == 0 {
			t.Fatalf("round %d pipeline produced 0 aggregated records", round)
		}
	}

	// Assertions:
	// 1. Raw log table has EXACTLY 100 records (0 duplicates)
	var rawCount int64
	db.Model(&mpentity.GatewaySpendLog{}).Where("tenant_id = ?", tenantID).Count(&rawCount)
	if rawCount != 100 {
		t.Fatalf("expected exactly 100 raw spend logs, got %d", rawCount)
	}

	// 2. Summary token count is EXACTLY 100 * 100 = 10,000 tokens (0% inflation/double-counting)
	startDateStr := start.AddDate(0, 0, -1).Format("2006-01-02")
	endDateStr := end.AddDate(0, 0, 1).Format("2006-01-02")

	summary, err := mpmetering.QueryUsageSummary(ctx, db, tenantID, startDateStr, endDateStr)
	if err != nil {
		t.Fatalf("query summary failed: %v", err)
	}

	expectedTotalTokens := int64(100 * 100)
	if summary.TotalTokens != expectedTotalTokens {
		t.Fatalf("Double counting detected on replay! TotalTokens=%d (expected %d)",
			summary.TotalTokens, expectedTotalTokens)
	}

	expectedCalls := int64(100)
	if summary.TotalCalls != expectedCalls {
		t.Fatalf("Double counting calls on replay! TotalCalls=%d (expected %d)",
			summary.TotalCalls, expectedCalls)
	}

	expectedSpend := 100 * 0.0015
	if math.Abs(summary.TotalSpend-expectedSpend) > 1e-4 {
		t.Fatalf("Double counting spend on replay! TotalSpend=%f (expected %f)",
			summary.TotalSpend, expectedSpend)
	}
}

// TestSecurityStability_MeteringDaemonWatermarkAndDowntimeRecovery validates the autonomous
// MeteringDaemon lifecycle: cold start, incremental polling, and multi-hour downtime catch-up.
func TestSecurityStability_MeteringDaemonWatermarkAndDowntimeRecovery(t *testing.T) {
	ctx := context.Background()
	db := setupStabilityTestDB(t)

	now := time.Now().UTC()
	tenantID := "daemon_e2e"

	// Mock gateway client with initial spend entries
	mockClient := &mockSpendLogClient{
		entries: []gateway.SpendLogEntry{
			{
				SpendID:          "daemon-entry-1",
				TeamID:           tenantID,
				Model:            "gpt-4o",
				TotalTokens:      1000,
				PromptTokens:     700,
				CompletionTokens: 300,
				Spend:            0.015,
				StartTime:        now.Add(-30 * time.Minute).Format("2006-01-02 15:04:05"),
			},
		},
	}

	collector := mpmetering.NewSpendLogCollector(db, mockClient)
	pipeline := mpmetering.NewUsagePipeline(db)

	daemon := mpmetering.NewMeteringDaemon(collector, pipeline,
		mpmetering.WithPollInterval(25*time.Millisecond),
		mpmetering.WithAggregationInterval(25*time.Millisecond),
		mpmetering.WithOverlapWindow(5*time.Minute),
	)

	// Step 1: Initial cold-start execution
	cRes1, err := daemon.PollOnce(ctx)
	if err != nil {
		t.Fatalf("daemon cold-start poll failed: %v", err)
	}
	if cRes1 == nil || cRes1.InsertedCount != 1 {
		t.Fatalf("expected 1 inserted log on cold start, got %+v", cRes1)
	}
	pRes1, err := daemon.AggregateOnce(ctx)
	if err != nil {
		t.Fatalf("daemon cold-start aggregation failed: %v", err)
	}
	if pRes1 == nil || pRes1.AggregatedRecords == 0 {
		t.Fatalf("expected non-zero aggregated records on cold start")
	}

	// Step 2: Incremental execution without new logs (no-op)
	cRes2, err := daemon.PollOnce(ctx)
	if err != nil {
		t.Fatalf("daemon second poll failed: %v", err)
	}
	if cRes2 == nil || cRes2.InsertedCount != 0 {
		t.Fatalf("expected 0 new inserted logs on idle poll, got %+v", cRes2)
	}

	// Step 3: Simulate 4-hour system downtime and backfilling
	downtimeEntries := []gateway.SpendLogEntry{
		{
			SpendID:          "daemon-entry-2",
			TeamID:           tenantID,
			Model:            "gpt-4o",
			TotalTokens:      500,
			PromptTokens:     350,
			CompletionTokens: 150,
			Spend:            0.0075,
			StartTime:        now.Add(-3 * time.Hour).Format("2006-01-02 15:04:05"),
		},
		{
			SpendID:          "daemon-entry-3",
			TeamID:           tenantID,
			Model:            "gpt-4o",
			TotalTokens:      500,
			PromptTokens:     350,
			CompletionTokens: 150,
			Spend:            0.0075,
			StartTime:        now.Add(-2 * time.Hour).Format("2006-01-02 15:04:05"),
		},
		{
			SpendID:          "daemon-entry-4",
			TeamID:           tenantID,
			Model:            "gpt-4o",
			TotalTokens:      500,
			PromptTokens:     350,
			CompletionTokens: 150,
			Spend:            0.0075,
			StartTime:        now.Add(-10 * time.Minute).Format("2006-01-02 15:04:05"),
		},
	}
	mockClient.entries = append(mockClient.entries, downtimeEntries...)

	cRes3, err := daemon.PollOnce(ctx)
	if err != nil {
		t.Fatalf("daemon catch-up poll failed: %v", err)
	}
	if cRes3 == nil || cRes3.InsertedCount != 3 {
		t.Fatalf("expected 3 recovered logs from downtime catch-up, got %+v", cRes3)
	}

	pRes3, err := daemon.AggregateOnce(ctx)
	if err != nil {
		t.Fatalf("daemon catch-up aggregation failed: %v", err)
	}
	if pRes3 == nil || pRes3.AggregatedRecords == 0 {
		t.Fatalf("expected non-zero aggregated records on catch-up")
	}

	// Verify all 4 entries are reflected in daily usage
	var dailyRec mpentity.TenantUsageDaily
	todayStr := now.Format("2006-01-02")
	if err := db.Where("tenant_id = ? AND usage_date = ?", tenantID, todayStr).First(&dailyRec).Error; err != nil {
		t.Fatalf("expected TenantUsageDaily record for today: %v", err)
	}
	expectedTotalTokens := int64(1000 + 500*3)
	if dailyRec.TotalTokens != expectedTotalTokens {
		t.Fatalf("expected %d total tokens after catch-up, got %d", expectedTotalTokens, dailyRec.TotalTokens)
	}

	// Step 4: Verify daemon graceful background lifecycle
	daemon.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	daemon.Stop()

	if daemon.TotalPolls() < 3 {
		t.Fatalf("expected total polls >= 3, got %d", daemon.TotalPolls())
	}
}
