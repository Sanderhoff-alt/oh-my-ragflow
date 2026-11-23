package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:admin_usage_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	ctx := context.Background()
	if err := migration.MigrateSpendLogSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate spend log schema: %v", err)
	}
	if err := migration.MigrateUsageSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate usage schema: %v", err)
	}

	if err := db.AutoMigrate(
		&rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{},
		&rentity.TenantModel{},
	); err != nil {
		t.Fatalf("failed to auto migrate tenant models: %v", err)
	}

	// Seed managed platform instance and model
	prov := rentity.TenantModelProvider{
		ID:           "prov-admin-1",
		TenantID:     "clean-tenant",
		ProviderName: "OpenAI-API-Compatible",
	}
	if err := db.Create(&prov).Error; err != nil {
		t.Fatalf("failed to seed provider: %v", err)
	}
	inst := rentity.TenantModelInstance{
		ID:           "inst-admin-1",
		ProviderID:   prov.ID,
		InstanceName: "platform",
	}
	if err := db.Create(&inst).Error; err != nil {
		t.Fatalf("failed to seed instance: %v", err)
	}
	if err := db.Create(&rentity.TenantModel{
		ID:         "tm-admin-1",
		ProviderID: prov.ID,
		InstanceID: inst.ID,
		ModelName:  "gpt-4o",
		Status:     "active",
	}).Error; err != nil {
		t.Fatalf("failed to seed model: %v", err)
	}

	return db
}

func setupTestServer(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	adminGroup := r.Group("/admin")
	RegisterModelPlatformAdminRoutes(adminGroup, db)

	return r
}

func TestAdmin_HealthCheck(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestServer(db)

	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/health", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.Code)
	}

	var res map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &res)
	if res["status"] != "ok" {
		t.Errorf("unexpected health status: %v", res)
	}
}

type mockAdminGatewayClient struct {
	healthErr error
}

func (m *mockAdminGatewayClient) GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	return nil, nil
}
func (m *mockAdminGatewayClient) UpdateKey(ctx context.Context, req *gateway.UpdateKeyRequest) (*gateway.UpdateKeyResponse, error) {
	return nil, nil
}
func (m *mockAdminGatewayClient) DeleteKey(ctx context.Context, keys []string) (*gateway.DeleteKeyResponse, error) {
	return nil, nil
}
func (m *mockAdminGatewayClient) GetKeyInfo(ctx context.Context, key string) (*gateway.KeyInfoResponse, error) {
	return nil, nil
}
func (m *mockAdminGatewayClient) GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error) {
	return nil, nil
}
func (m *mockAdminGatewayClient) HealthCheck(ctx context.Context) error {
	return m.healthErr
}

func TestAdmin_HealthCheck_DeepGatewayProbing(t *testing.T) {
	db := setupTestDB(t)

	// 1. Healthy gateway
	gin.SetMode(gin.TestMode)
	rHealthy := gin.New()
	adminHealthy := rHealthy.Group("/admin")
	RegisterModelPlatformAdminRoutesWithGateway(adminHealthy, &mockAdminGatewayClient{healthErr: nil}, db)

	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/health", nil)
	respHealthy := httptest.NewRecorder()
	rHealthy.ServeHTTP(respHealthy, req)

	if respHealthy.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for healthy gateway, got %d", respHealthy.Code)
	}
	var resHealthy map[string]interface{}
	json.Unmarshal(respHealthy.Body.Bytes(), &resHealthy)
	if resHealthy["status"] != "ok" || resHealthy["gateway"] != "healthy" {
		t.Errorf("unexpected healthy payload: %+v", resHealthy)
	}

	// 2. Unhealthy gateway
	rUnhealthy := gin.New()
	adminUnhealthy := rUnhealthy.Group("/admin")
	RegisterModelPlatformAdminRoutesWithGateway(adminUnhealthy, &mockAdminGatewayClient{healthErr: fmt.Errorf("connection refused: 127.0.0.1:4000")}, db)

	respUnhealthy := httptest.NewRecorder()
	rUnhealthy.ServeHTTP(respUnhealthy, req)

	if respUnhealthy.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable for dead gateway, got %d", respUnhealthy.Code)
	}
	var resUnhealthy map[string]interface{}
	json.Unmarshal(respUnhealthy.Body.Bytes(), &resUnhealthy)
	if resUnhealthy["status"] != "degraded" || resUnhealthy["gateway"] != "unreachable" {
		t.Errorf("unexpected unhealthy payload: %+v", resUnhealthy)
	}
}

func TestAdmin_GetDailyUsage_PaginationAndFilter(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestServer(db)

	// Seed daily records
	records := []entity.TenantUsageDaily{
		{
			TenantID:    "tenant-1",
			ModelName:   "gpt-4o",
			UsageDate:   "2026-11-14",
			TotalTokens: 100,
			TotalSpend:  0.001,
		},
		{
			TenantID:    "tenant-1",
			ModelName:   "gpt-4o",
			UsageDate:   "2026-11-15",
			TotalTokens: 200,
			TotalSpend:  0.002,
		},
		{
			TenantID:    "tenant-2",
			ModelName:   "gpt-4o",
			UsageDate:   "2026-11-15",
			TotalTokens: 300,
			TotalSpend:  0.003,
		},
		{
			TenantID:    "tenant-2",
			ModelName:   "text-embedding-3-small",
			UsageDate:   "2026-11-15",
			TotalTokens: 50,
			TotalSpend:  0.0001,
		},
	}
	for _, rec := range records {
		db.Create(&rec)
	}

	// 1. Unfiltered query
	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", resp.Code, resp.Body.String())
	}
	var res map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &res)
	data := res["data"].(map[string]interface{})
	if total := data["total"].(float64); total != 4 {
		t.Errorf("expected total 4, got %v", total)
	}

	// 2. Filter by tenant_id
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily?tenant_id=tenant-1", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var resTenant map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resTenant)
	dataTenant := resTenant["data"].(map[string]interface{})
	if total := dataTenant["total"].(float64); total != 2 {
		t.Errorf("expected 2 records for tenant-1, got %v", total)
	}

	// 3. Filter by model_name
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily?model_name=text-embedding-3-small", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var resModel map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resModel)
	dataModel := resModel["data"].(map[string]interface{})
	if total := dataModel["total"].(float64); total != 1 {
		t.Errorf("expected 1 record for embedding model, got %v", total)
	}

	// 4. Pagination (page 1)
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily?page=1&page_size=2", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	var resPage map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resPage)
	dataPage := resPage["data"].(map[string]interface{})
	items := dataPage["items"].([]interface{})
	if len(items) != 2 {
		t.Errorf("expected 2 items on page 1, got %d", len(items))
	}
	if dataPage["total"].(float64) != 4 {
		t.Errorf("expected total 4, got %v", dataPage["total"])
	}
	// Verify usage_date DESC ordering
	firstItem := items[0].(map[string]interface{})
	if firstItem["usage_date"] != "2026-11-15" {
		t.Errorf("expected usage_date 2026-11-15 for first item, got %v", firstItem["usage_date"])
	}

	// 5. Pagination (page 2)
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily?page=2&page_size=2", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	json.Unmarshal(resp.Body.Bytes(), &resPage)
	dataPage2 := resPage["data"].(map[string]interface{})
	items2 := dataPage2["items"].([]interface{})
	if len(items2) != 2 {
		t.Errorf("expected 2 items on page 2, got %d", len(items2))
	}
	lastItem := items2[1].(map[string]interface{})
	if lastItem["usage_date"] != "2026-11-14" {
		t.Errorf("expected usage_date 2026-11-14 for last item, got %v", lastItem["usage_date"])
	}

	// 6. Page size clamp (cap at 100)
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/daily?page_size=500", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	json.Unmarshal(resp.Body.Bytes(), &resPage)
	dataPageCap := resPage["data"].(map[string]interface{})
	if dataPageCap["page_size"].(float64) != 100 {
		t.Errorf("expected clamped page_size 100, got %v", dataPageCap["page_size"])
	}
}

func TestAdmin_GetUsageSummary_TenantAndGlobal(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestServer(db)

	records := []entity.TenantUsageDaily{
		{
			TenantID:         "tenant-a",
			ModelName:        "gpt-4o",
			UsageDate:        "2026-11-15",
			CallCount:        10,
			TotalTokens:      1000,
			PromptTokens:     800,
			CompletionTokens: 200,
			TotalSpend:       0.010,
		},
		{
			TenantID:         "tenant-b",
			ModelName:        "gpt-4o",
			UsageDate:        "2026-11-15",
			CallCount:        5,
			TotalTokens:      500,
			PromptTokens:     400,
			CompletionTokens: 100,
			TotalSpend:       0.005,
		},
	}
	for _, rec := range records {
		db.Create(&rec)
	}

	// 1. Tenant-specific summary
	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/usage/summary?tenant_id=tenant-a", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", resp.Code, resp.Body.String())
	}
	var resTenant map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resTenant)
	dataTenant := resTenant["data"].(map[string]interface{})
	if dataTenant["total_tokens"].(float64) != 1000 {
		t.Errorf("expected 1000 tokens for tenant-a, got %v", dataTenant["total_tokens"])
	}
	if dataTenant["total_calls"].(float64) != 10 {
		t.Errorf("expected 10 calls for tenant-a, got %v", dataTenant["total_calls"])
	}

	// 2. Global summary across all tenants
	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/summary", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for global summary, got %d", resp.Code)
	}
	var resGlobal map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resGlobal)
	dataGlobal := resGlobal["data"].(map[string]interface{})
	if dataGlobal["total_tokens"].(float64) != 1500 {
		t.Errorf("expected 1500 global tokens, got %v", dataGlobal["total_tokens"])
	}
	if dataGlobal["total_calls"].(float64) != 15 {
		t.Errorf("expected 15 global calls, got %v", dataGlobal["total_calls"])
	}
}

func TestAdmin_AuditUsage(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestServer(db)

	// 1. Missing tenant_id parameter
	req, _ := http.NewRequest(http.MethodGet, "/admin/model-platform/usage/audit", nil)
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for missing tenant_id, got %d", resp.Code)
	}

	// 2. Clean tenant with authorized platform-managed spend
	if err := db.Create(&entity.GatewaySpendLog{
		SpendID:     "s-clean-admin",
		TenantID:    "clean-tenant",
		ModelName:   "gpt-4o",
		Spend:       0.01,
		RequestTime: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("failed to seed clean-tenant spend log: %v", err)
	}

	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/audit?tenant_id=clean-tenant", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.Code)
	}
	var resClean map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resClean)
	dataClean := resClean["data"].(map[string]interface{})
	if dataClean["audit_passed"].(bool) != true {
		t.Errorf("expected audit_passed=true, got %v", dataClean["audit_passed"])
	}
	if dataClean["managed_spend_total"].(float64) != 0.01 {
		t.Errorf("expected managed_spend_total 0.01, got %v", dataClean["managed_spend_total"])
	}

	// 3. Compromised tenant with rogue model
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "s-rogue-admin",
		TenantID:    "rogue-tenant",
		ModelName:   "tampered-private-model",
		Spend:       0.08,
		RequestTime: time.Now().UTC(),
	})

	req, _ = http.NewRequest(http.MethodGet, "/admin/model-platform/usage/audit?tenant_id=rogue-tenant", nil)
	resp = httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", resp.Code)
	}
	var resRogue map[string]interface{}
	json.Unmarshal(resp.Body.Bytes(), &resRogue)
	dataRogue := resRogue["data"].(map[string]interface{})
	if dataRogue["audit_passed"].(bool) != false {
		t.Errorf("expected audit_passed=false for rogue tenant, got %v", dataRogue["audit_passed"])
	}
}
