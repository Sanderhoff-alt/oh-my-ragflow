package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	mpadmin "ragflow/extensions/model-platform/backend/admin"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	mpmetering "ragflow/extensions/model-platform/backend/metering"
	mpreconciler "ragflow/extensions/model-platform/backend/reconciler"
	"ragflow/internal/common"
	rentity "ragflow/internal/entity"
	"ragflow/internal/entity/models"

	"github.com/gin-gonic/gin"
)

// TestE2E_Admin_GatewayHealthCheck verifies that the Admin /model-platform/health endpoint
// conducts deep health checks against the LiteLLM gateway, correctly reflecting healthy
// status and degrading when the downstream gateway becomes unreachable.
func TestE2E_Admin_GatewayHealthCheck(t *testing.T) {
	db := setupE2ETestDB(t)
	server, _ := setupMockLiteLLMGateway(t)
	defer server.Close()

	gwClient := gateway.NewClient(gateway.GatewayConfig{BaseURL: server.URL})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(adminGroup, gwClient, db)

	// 1. Assert healthy gateway status
	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/health", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for healthy gateway, got %d", resp.Code)
	}

	var resHealthy map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &resHealthy); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if resHealthy["status"] != "ok" || resHealthy["gateway"] != "healthy" {
		t.Errorf("unexpected healthy payload: %+v", resHealthy)
	}

	// 2. Assert degraded status when downstream gateway is dead
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gateway unavailable", http.StatusServiceUnavailable)
	}))
	defer deadServer.Close()
	deadClient := gateway.NewClient(gateway.GatewayConfig{
		BaseURL: deadServer.URL,
		Timeout: 50 * time.Millisecond,
	})
	rDegraded := gin.New()
	adminDegraded := rDegraded.Group("/admin")
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(adminDegraded, deadClient, db)

	respDegraded := httptest.NewRecorder()
	rDegraded.ServeHTTP(respDegraded, req)

	if respDegraded.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when gateway is dead, got %d", respDegraded.Code)
	}

	var resDegraded map[string]interface{}
	if err := json.Unmarshal(respDegraded.Body.Bytes(), &resDegraded); err != nil {
		t.Fatalf("failed to decode degraded response: %v", err)
	}
	if resDegraded["status"] != "degraded" || resDegraded["gateway"] != "unreachable" {
		t.Errorf("unexpected degraded payload: %+v", resDegraded)
	}
}

// TestE2E_Admin_FullBillingAndAuditClosedLoop proves the complete closed loop:
// 1. Tenant creation and managed key provisioning via Reconciler.
// 2. Native upstream driver chat execution generating real spend logs in LiteLLM.
// 3. MeteringDaemon ingestion and daily aggregation into database.
// 4. Admin API endpoints (/usage/daily, /usage/summary, /usage/audit) verifying end-to-end consistency.
func TestE2E_Admin_FullBillingAndAuditClosedLoop(t *testing.T) {
	db := setupE2ETestDB(t)
	server, state := setupMockLiteLLMGateway(t)
	defer server.Close()

	ctx := context.Background()
	tenantID := "tenant-admin-closed-loop"
	userID := "user-admin-closed-loop"

	// 1. Seed tenant and user in database
	statusValid := "1"
	tName := "Admin Closed-Loop Tenant"
	db.Create(&rentity.Tenant{
		ID:     tenantID,
		Name:   &tName,
		Status: &statusValid,
	})
	db.Create(&rentity.User{
		ID:       userID,
		Nickname: "Admin User",
		Status:   &statusValid,
	})
	db.Create(&rentity.UserTenant{
		ID:       "ut-" + tenantID,
		UserID:   userID,
		TenantID: tenantID,
		Role:     "owner",
		Status:   &statusValid,
	})

	// 2. Provision managed instance and virtual key
	gwClient := gateway.NewClient(gateway.GatewayConfig{BaseURL: server.URL})
	reconciler := mpreconciler.NewTenantReconciler(
		gwClient,
		server.URL,
		[]string{"gpt-4o-mini", "text-embedding-3-small"},
	)
	if err := reconciler.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("reconciler failed: %v", err)
	}

	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ? AND provider_name = ?", tenantID, "OpenAI-API-Compatible").First(&provider).Error; err != nil {
		t.Fatalf("failed to find provisioned provider: %v", err)
	}

	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, "platform").First(&instance).Error; err != nil {
		t.Fatalf("failed to find provisioned instance: %v", err)
	}
	if instance.APIKey == "" {
		t.Fatal("expected non-empty virtual key for managed instance")
	}

	// 3. Execute inference with native driver against gateway using virtual key
	urlSuffix := models.URLSuffix{
		Chat:      "chat/completions",
		Embedding: "embeddings",
	}
	driver := models.NewOpenAIAPICompatibleModel(map[string]string{}, urlSuffix)

	apiConfig := &models.APIConfig{
		ApiKey:  &instance.APIKey,
		BaseURL: &server.URL,
	}

	var chatUsage common.ModelUsage
	chatMessages := []models.Message{
		{Role: "user", Content: "Hello Admin Closed Loop"},
	}
	chatResp, err := driver.ChatWithMessages(ctx, "gpt-4o-mini", chatMessages, apiConfig, &models.ChatConfig{}, &chatUsage)
	if err != nil {
		t.Fatalf("chat request failed: %v", err)
	}
	if chatResp == nil || chatResp.Answer == nil {
		t.Fatal("expected non-nil chat response")
	}

	// Verify gateway recorded the spend log entry
	state.mu.Lock()
	recordedLogs := len(state.spendLogs)
	state.mu.Unlock()
	if recordedLogs == 0 {
		t.Fatal("expected mock gateway to record spend log entry")
	}

	// 4. Ingest and aggregate spend logs via MeteringDaemon
	collector := mpmetering.NewSpendLogCollector(db, gwClient)
	pipeline := mpmetering.NewUsagePipeline(db)
	daemon := mpmetering.NewMeteringDaemon(db, collector, pipeline)

	collectRes, err := daemon.PollOnce(ctx)
	if err != nil {
		t.Fatalf("daemon PollOnce failed: %v", err)
	}
	if collectRes.InsertedCount == 0 {
		t.Fatalf("expected at least 1 inserted spend log, got %d", collectRes.InsertedCount)
	}

	aggRes, err := daemon.AggregateOnce(ctx)
	if err != nil {
		t.Fatalf("daemon AggregateOnce failed: %v", err)
	}
	if aggRes.AggregatedRecords == 0 {
		t.Fatalf("expected at least 1 aggregated record, got %d", aggRes.AggregatedRecords)
	}

	// 5. Query and assert Admin HTTP API endpoints
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(adminGroup, gwClient, db)

	expectedSpend := 0.0002

	// 5.1 GET /admin/model-platform/usage/daily
	{
		url := fmt.Sprintf("/admin/model-platform/usage/daily?tenant_id=%s", tenantID)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for daily usage query, got %d: %s", resp.Code, resp.Body.String())
		}

		var payload struct {
			Code int `json:"code"`
			Data struct {
				Total int64                       `json:"total"`
				Items []mpentity.TenantUsageDaily `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode daily usage payload: %v", err)
		}
		if payload.Data.Total != 1 || len(payload.Data.Items) != 1 {
			t.Fatalf("expected 1 daily usage item, got total=%d items=%d", payload.Data.Total, len(payload.Data.Items))
		}
		item := payload.Data.Items[0]
		if item.TenantID != tenantID || item.ModelName != "gpt-4o-mini" {
			t.Errorf("unexpected daily usage item: %+v", item)
		}
		if item.TotalTokens != 23 {
			t.Errorf("expected 23 total tokens in daily usage, got %d", item.TotalTokens)
		}
	}

	// 5.2 GET /admin/model-platform/usage/summary
	{
		url := fmt.Sprintf("/admin/model-platform/usage/summary?tenant_id=%s", tenantID)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for usage summary, got %d: %s", resp.Code, resp.Body.String())
		}

		var payload struct {
			Code int                     `json:"code"`
			Data mpmetering.UsageSummary `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode summary payload: %v", err)
		}
		if payload.Data.TotalTokens != 23 {
			t.Errorf("expected 23 total tokens in summary, got %d", payload.Data.TotalTokens)
		}
		if payload.Data.TotalCalls != 1 {
			t.Errorf("expected 1 total call in summary, got %d", payload.Data.TotalCalls)
		}
		if math.Abs(payload.Data.TotalSpend-expectedSpend) > 1e-6 {
			t.Errorf("expected %f total spend in summary, got %f", expectedSpend, payload.Data.TotalSpend)
		}
	}

	// 5.3 GET /admin/model-platform/usage/audit (Zero-Billing Isolation Audit)
	{
		url := fmt.Sprintf("/admin/model-platform/usage/audit?tenant_id=%s", tenantID)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)

		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for usage audit, got %d: %s", resp.Code, resp.Body.String())
		}

		var payload struct {
			Code int                             `json:"code"`
			Data mpmetering.AuditIsolationReport `json:"data"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatalf("failed to decode audit payload: %v", err)
		}
		if !payload.Data.AuditPassed {
			t.Errorf("expected clean zero-billing audit (audit_passed=true), got false: %+v", payload.Data)
		}
		if len(payload.Data.Violations) != 0 {
			t.Errorf("expected 0 violations, got %v", payload.Data.Violations)
		}
		if math.Abs(payload.Data.ManagedSpendTotal-expectedSpend) > 1e-6 {
			t.Errorf("expected %f managed spend, got %f", expectedSpend, payload.Data.ManagedSpendTotal)
		}
	}
}

// TestE2E_Admin_AuditViolationDetection verifies that if an unauthorized/unmanaged model
// somehow appears in the gateway spend logs for a tenant, the Admin audit endpoint
// actively flags the security violation.
func TestE2E_Admin_AuditViolationDetection(t *testing.T) {
	db := setupE2ETestDB(t)

	tenantID := "tenant-admin-violation"

	// 1. Seed tenant and managed provider/model in DB
	prov := rentity.TenantModelProvider{
		ID:           "prov-viol-1",
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	}
	db.Create(&prov)
	inst := rentity.TenantModelInstance{
		ID:           "inst-viol-1",
		ProviderID:   prov.ID,
		InstanceName: "platform",
	}
	db.Create(&inst)
	db.Create(&rentity.TenantModel{
		ID:         "tm-viol-1",
		ProviderID: prov.ID,
		InstanceID: inst.ID,
		ModelName:  "gpt-4o-mini",
		Status:     "active",
	})

	// 2. Inject an unauthorized model directly into platform_gateway_spend_log
	now := time.Now().UTC()
	db.Create(&mpentity.GatewaySpendLog{
		SpendID:          fmt.Sprintf("spend-viol-%d", now.UnixNano()),
		TenantID:         tenantID,
		ModelName:        "unauthorized-shadow-model",
		CallType:         "chat",
		Spend:            0.088,
		TotalTokens:      150,
		PromptTokens:     100,
		CompletionTokens: 50,
		RequestTime:      now.Add(-1 * time.Minute),
	})

	// 3. Query Admin Audit endpoint
	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	mpadmin.RegisterModelPlatformAdminRoutes(adminGroup, db)

	url := fmt.Sprintf("/admin/model-platform/usage/audit?tenant_id=%s", tenantID)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", resp.Code, resp.Body.String())
	}

	var payload struct {
		Code int                             `json:"code"`
		Data mpmetering.AuditIsolationReport `json:"data"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode audit payload: %v", err)
	}

	if payload.Data.AuditPassed {
		t.Fatal("expected audit failure (audit_passed=false), but got clean")
	}
	if payload.Data.SelfOwnedSpendTotal <= 0 {
		t.Errorf("expected positive violating spend, got %f", payload.Data.SelfOwnedSpendTotal)
	}

	foundViolatingModel := false
	for _, m := range payload.Data.UnmanagedModelsDetected {
		if m == "unauthorized-shadow-model" {
			foundViolatingModel = true
			break
		}
	}
	if !foundViolatingModel {
		t.Errorf("expected violating models to contain 'unauthorized-shadow-model', got: %v", payload.Data.UnmanagedModelsDetected)
	}
}

// TestE2E_Admin_LiveGatewayCluster verifies Admin API deep health check
// against a live LiteLLM gateway and SQLite-backed Admin queries when active.
func TestE2E_Admin_LiveGatewayCluster(t *testing.T) {
	liveGatewayURL := os.Getenv("LITELLM_TEST_URL")
	if liveGatewayURL == "" {
		liveGatewayURL = "http://127.0.0.1:4000"
	}
	requireLiveGateway := os.Getenv("REQUIRE_LIVE_GATEWAY") == "true"

	// Check if live LiteLLM container is active
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(liveGatewayURL + "/health/liveliness")
	if err != nil || resp.StatusCode != http.StatusOK {
		if requireLiveGateway {
			t.Fatalf("FATAL: REQUIRE_LIVE_GATEWAY is true, but live gateway container is unreachable on %s: %v", liveGatewayURL, err)
		}
		t.Skipf("Live LiteLLM gateway unreachable on %s (%v); skipping live Admin test (run setup_env.sh --start to run).", liveGatewayURL, err)
		return
	}
	_ = resp.Body.Close()

	db := setupE2ETestDB(t)
	liveClient := gateway.NewClient(gateway.GatewayConfig{
		BaseURL:   liveGatewayURL,
		MasterKey: "sk-master-e2e-platform-test-key",
	})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	adminGroup := r.Group("/admin")
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(adminGroup, liveClient, db)

	// 1. Assert Admin /health probes live container successfully
	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/health", nil)
	healthResp := httptest.NewRecorder()
	r.ServeHTTP(healthResp, req)

	if healthResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from live health check, got %d", healthResp.Code)
	}

	var healthPayload map[string]interface{}
	json.Unmarshal(healthResp.Body.Bytes(), &healthPayload)
	if healthPayload["gateway"] != "healthy" {
		t.Errorf("expected live gateway status to be 'healthy', got: %v", healthPayload["gateway"])
	}

	// 2. Assert Admin usage and summary endpoints respond correctly
	reqDaily, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily", nil)
	dailyResp := httptest.NewRecorder()
	r.ServeHTTP(dailyResp, reqDaily)

	if dailyResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for daily usage query, got %d", dailyResp.Code)
	}

	reqSummary, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/usage/summary", nil)
	summaryResp := httptest.NewRecorder()
	r.ServeHTTP(summaryResp, reqSummary)

	if summaryResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for summary query, got %d", summaryResp.Code)
	}
}
