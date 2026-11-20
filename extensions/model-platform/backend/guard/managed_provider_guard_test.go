package guard

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ragflow/internal/common"
	"ragflow/internal/dao"
	"ragflow/internal/entity"
	"ragflow/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func setupTestRouter() *gin.Engine {
	r := gin.New()
	r.Use(ManagedProviderGuardMiddleware())

	// Exact upstream-aligned provider routes under /api/v1/providers
	providers := r.Group("/api/v1/providers")
	{
		providers.PUT("", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"created_provider": "ok"})
		})
		providers.GET("/:provider_id_or_name", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "provider": c.Param("provider_id_or_name")})
		})
		providers.DELETE("/:provider_id_or_name", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"deleted_provider": c.Param("provider_id_or_name")})
		})
		providers.GET("/:provider_id_or_name/instances/:instance_id_or_name", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"status": "ok", "instance": c.Param("instance_id_or_name")})
		})
		providers.PUT("/:provider_id_or_name/instances/:instance_id_or_name", func(c *gin.Context) {
			var req service.AlterProviderInstanceRequest
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"updated_instance": c.Param("instance_id_or_name")})
		})
		// Upstream real instance deletion route: DELETE /:provider_id_or_name/instances with JSON body {"instances": [...]}
		providers.POST("/:provider_id_or_name/instances", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"created_instance": "ok"})
		})
		providers.DELETE("/:provider_id_or_name/instances", func(c *gin.Context) {
			var req struct {
				Instances []string `json:"instances"`
			}
			if err := c.ShouldBindJSON(&req); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"deleted_instances": req.Instances})
		})
		providers.PATCH("/:provider_id_or_name/instances/:instance_id_or_name/models/*model_name", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"patched_model": c.Param("model_name")})
		})
		providers.POST("/:provider_id_or_name/instances/:instance_id_or_name/models", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"added_model": "ok"})
		})
	}

	// Non-provider routes for negative verification
	r.DELETE("/api/v1/dataset/:dataset_id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"deleted_dataset": c.Param("dataset_id")})
	})
	r.PUT("/api/v1/datasets/:dataset_id/artifacts/:page_type/*slug", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"artifact": "ok"})
	})

	// User setting and admin model provider routes:
	// /v1/user/setting/model/provider, /api/v1/user/setting/model/provider, /api/v1/admin/model/provider
	for _, prefix := range []string{"/v1/user/setting/model/provider", "/api/v1/user/setting/model/provider", "/api/v1/admin/model/provider"} {
		p := prefix
		r.DELETE(p, func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"deleted": "ok"})
		})
		r.DELETE(p+"/:provider_id_or_name", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"deleted": c.Param("provider_id_or_name")})
		})
		r.PUT(p, func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"updated": "ok"})
		})
	}

	return r
}

func TestManagedProviderGuard_StatelessChecks(t *testing.T) {
	t.Setenv("MAX_JSON_CONTENT_LENGTH", "1048576")
	originalDB := dao.DB
	dao.DB = nil
	defer func() {
		dao.DB = originalDB
	}()

	router := setupTestRouter()

	tests := []struct {
		name           string
		method         string
		url            string
		body           string
		expectedStatus int
		expectBlocked  bool
	}{
		{
			name:           "GET requests to platform provider should pass through",
			method:         http.MethodGet,
			url:            "/api/v1/providers/platform",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "GET requests to platform instance should pass through",
			method:         http.MethodGet,
			url:            "/api/v1/providers/OpenAI/instances/platform",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE requests to non-provider paths should pass through",
			method:         http.MethodDelete,
			url:            "/api/v1/dataset/platform",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE targeting platform provider route should be blocked (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT targeting platform instance should be blocked (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT unmanaged instance attempting to rename to 'platform' should be blocked (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst",
			body:           `{"instance_name":"platform","api_key":"sk-custom"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT unmanaged instance attempting to rename to 'PLATFORM' (case-insensitive) should be blocked (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst",
			body:           `{"instance_name":"PLATFORM"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT unmanaged instance rename to ordinary name with credential containing 'platform' should pass",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst",
			body:           `{"instance_name":"custom-inst-v2","api_key":"sk-platform-token","model_info":[]}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "PATCH targeting platform instance model should be blocked (403)",
			method:         http.MethodPatch,
			url:            "/api/v1/providers/OpenAI/instances/platform/models/gpt-4o",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE upstream instances route with 'platform' in body should be blocked (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"instances":["platform"]}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE upstream instances route with unmanaged name should pass through",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"instances":["my-custom-key"]}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE targeting platform via /v1/user/setting/model/provider/platform should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider/platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE targeting platform via /api/v1/user/setting/model/provider/platform should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/user/setting/model/provider/platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE targeting platform via /api/v1/admin/model/provider/platform should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/admin/model/provider/platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE /v1/user/setting/model/provider?name=platform query param should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE /api/v1/user/setting/model/provider?name=platform query param should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/user/setting/model/provider?name=platform",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE /v1/user/setting/model/provider with JSON body targeting platform should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider",
			body:           `{"name":"platform"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE /v1/user/setting/model/provider with tenant-owned provider should pass through",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=custom-provider",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE /api/v1/providers/platform-custom should pass through (path boundary check)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/platform-custom",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "PATCH /api/v1/providers/platform/instances/inst1/models/gpt-4o should be BLOCKED (403) (descendant check)",
			method:         http.MethodPatch,
			url:            "/api/v1/providers/platform/instances/inst1/models/gpt-4o",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PATCH /api/v1/providers/my-custom/instances/inst1/models/gpt-4o should pass through (descendant custom control)",
			method:         http.MethodPatch,
			url:            "/api/v1/providers/my-custom/instances/inst1/models/gpt-4o",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE /v1/user/setting/model/provider?name=platform-custom should pass through",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=platform-custom",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "POST instance create with 'platform' instance_name should be BLOCKED (403)",
			method:         http.MethodPost,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"instance_name":"platform"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "POST instance create with 'PLATFORM' (case-insensitive INSTANCE_NAME) should be BLOCKED (403)",
			method:         http.MethodPost,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"INSTANCE_NAME":"PLATFORM"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "POST instance create with valid custom name should pass through (200)",
			method:         http.MethodPost,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"instance_name":"my-custom-llm"}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "PUT to dataset artifact with 'provider/platform' slug should pass through (200)",
			method:         http.MethodPut,
			url:            "/api/v1/datasets/d1/artifacts/wiki/provider/platform",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "POST create model under platform instance should be BLOCKED (403)",
			method:         http.MethodPost,
			url:            "/api/v1/providers/OpenAI/instances/platform/models",
			body:           `{"model_name":"my-gpt"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "POST create model under unmanaged instance should pass through (200)",
			method:         http.MethodPost,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst/models",
			body:           `{"model_name":"my-gpt"}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE with trailing non-whitespace data after JSON should be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI/instances",
			body:           `{"instances":["custom-inst"]} trailing_garbage`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PATCH model on unmanaged instance with extraneous name:'platform' should pass through (200)",
			method:         http.MethodPatch,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst/models/gpt-4o",
			body:           `{"status":"inactive","name":"platform"}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "PUT rename instance with conflicting case-insensitive instance_name spellings should be BLOCKED (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst",
			body:           `{"instance_name":"custom-inst-v2","Instance_Name":"platform","model_info":[]}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT create provider with 'platform' provider_name should be BLOCKED (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers",
			body:           `{"provider_name":"platform"}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT create provider with valid custom name should pass through (200)",
			method:         http.MethodPut,
			url:            "/api/v1/providers",
			body:           `{"provider_name":"My-Custom-LLM"}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "PUT rename instance currently named 'models' to 'platform' should be BLOCKED (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/models",
			body:           `{"instance_name":"platform","model_info":[]}`,
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT rename instance with 'instance_name' and extra 'instanceName' should pass through (200)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI/instances/custom-inst",
			body:           `{"instance_name":"custom-v2","instanceName":"display","model_info":[]}`,
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req, _ = http.NewRequest(tt.method, tt.url, bytes.NewBufferString(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, _ = http.NewRequest(tt.method, tt.url, nil)
			}

			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d. Body: %s", tt.expectedStatus, w.Code, w.Body.String())
			}

			if tt.expectBlocked {
				var resp map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to parse json response: %v", err)
				}
				if resp["code"] != float64(403) {
					t.Errorf("expected response code 403, got %v", resp["code"])
				}
				if resp["message"] != ErrMsgManagedProviderForbidden {
					t.Errorf("expected error message %q, got %q", ErrMsgManagedProviderForbidden, resp["message"])
				}
			}
		})
	}
}

func TestManagedProviderGuard_DatabaseBackedChecks(t *testing.T) {
	t.Setenv("MAX_JSON_CONTENT_LENGTH", "1048576")
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite in-memory db: %v", err)
	}

	if err := db.AutoMigrate(&entity.TenantModelProvider{}, &entity.TenantModelInstance{}); err != nil {
		t.Fatalf("failed to auto migrate: %v", err)
	}

	var queryCount int64
	_ = db.Callback().Query().After("gorm:query").Register("test:count_queries", func(tx *gorm.DB) {
		if tx.DryRun || tx.Statement.DryRun {
			return
		}
		atomic.AddInt64(&queryCount, 1)
	})

	ptr := func(i int) *int { return &i }

	p1 := entity.TenantModelProvider{
		ID:           "prov-t1-openai",
		ProviderName: "OpenAI-API-Compatible",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	p2 := entity.TenantModelProvider{
		ID:           "prov-t1-local",
		ProviderName: "Custom-LocalAI",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	p3 := entity.TenantModelProvider{
		ID:           "prov-t2-openai",
		ProviderName: "OpenAI-API-Compatible",
		TenantID:     "tenant-2",
		UserID:       "user-2",
	}

	instManaged := entity.TenantModelInstance{
		ID:           "uuid-managed-inst-1",
		InstanceName: "managed-openai",
		ProviderID:   "prov-t1-openai",
		APIKey:       "secret",
		Extra:        `{"is_managed": "true", "model_type": "chat"}`, // json with whitespace string representation
	}
	instTenantInSameProv := entity.TenantModelInstance{
		ID:           "uuid-tenant-inst-1",
		InstanceName: "my-openai-key",
		ProviderID:   "prov-t1-openai",
		APIKey:       "custom-key",
		Extra:        `{"is_managed": "false"}`,
	}
	instTenantInOtherProv := entity.TenantModelInstance{
		ID:           "uuid-tenant-inst-2",
		InstanceName: "my-local-model",
		ProviderID:   "prov-t1-local",
		APIKey:       "local-key",
		Extra:        `{}`,
	}
	instTenantInSameProv2 := entity.TenantModelInstance{
		ID:           "uuid-tenant-inst-3",
		InstanceName: "my-openai-key-2",
		ProviderID:   "prov-t1-openai",
		APIKey:       "custom-key-2",
		Extra:        `{"is_managed": "false"}`,
	}
	instSameNameTenant1 := entity.TenantModelInstance{
		ID:           "uuid-t1-shared-name",
		InstanceName: "shared-instance-name",
		ProviderID:   "prov-t1-local",
		APIKey:       "key-t1",
		Extra:        `{"is_managed": "false"}`,
	}
	instSameNameTenant2Managed := entity.TenantModelInstance{
		ID:           "uuid-t2-shared-name-managed",
		InstanceName: "shared-instance-name",
		ProviderID:   "prov-t2-managed-other",
		APIKey:       "key-t2",
		Extra:        `{"is_managed": "true"}`,
	}
	instT2 := entity.TenantModelInstance{
		ID:           "uuid-t2-inst",
		InstanceName: "t2-key",
		ProviderID:   "prov-t2-openai",
		APIKey:       "t2-key",
		Extra:        `{"is_managed": "false"}`,
	}
	p4 := entity.TenantModelProvider{
		ID:           "prov-t1-nested",
		ProviderName: "Custom-Nested-Meta",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	instNestedMeta := entity.TenantModelInstance{
		ID:           "uuid-nested-inst",
		InstanceName: "nested-meta-inst",
		ProviderID:   "prov-t1-nested",
		APIKey:       "nested-key",
		Extra:        `{"is_managed": "false", "metadata": {"is_managed": "true"}}`,
	}

	p5 := entity.TenantModelProvider{
		ID:           "prov-t1-instance-proxy",
		ProviderName: "instance-proxy",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	instManagedProxy := entity.TenantModelInstance{
		ID:           "uuid-inst-proxy-managed",
		InstanceName: "managed-proxy-inst",
		ProviderID:   "prov-t1-instance-proxy",
		APIKey:       "proxy-secret",
		Extra:        `{"is_managed": "true"}`,
	}

	p6 := entity.TenantModelProvider{
		ID:           "prov-t1-exact-instance",
		ProviderName: "instance",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	instManagedExactInstance := entity.TenantModelInstance{
		ID:           "uuid-inst-exact-instance-managed",
		InstanceName: "exact-instance-inst",
		ProviderID:   "prov-t1-exact-instance",
		APIKey:       "exact-secret-1",
		Extra:        `{"is_managed": "true"}`,
	}

	p7 := entity.TenantModelProvider{
		ID:           "prov-t1-exact-instances",
		ProviderName: "instances",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	instManagedExactInstances := entity.TenantModelInstance{
		ID:           "uuid-inst-exact-instances-managed",
		InstanceName: "exact-instances-inst",
		ProviderID:   "prov-t1-exact-instances",
		APIKey:       "exact-secret-2",
		Extra:        `{"is_managed": "true"}`,
	}

	p8 := entity.TenantModelProvider{
		ID:           "prov-t1-unmanaged-custom",
		ProviderName: "instance-custom",
		TenantID:     "tenant-1",
		UserID:       "user-1",
	}
	p9 := entity.TenantModelProvider{
		ID:           "prov-t2-managed-other",
		ProviderName: "Custom-LocalAI",
		TenantID:     "tenant-2",
		UserID:       "user-2",
	}
	instUnmanagedCustom := entity.TenantModelInstance{
		ID:           "uuid-inst-custom",
		InstanceName: "custom-inst",
		ProviderID:   "prov-t1-unmanaged-custom",
		APIKey:       "custom-secret",
		Extra:        `{"is_managed": "false"}`,
	}

	fixtures := []struct {
		name  string
		model interface{}
	}{
		{"p1", &p1}, {"p2", &p2}, {"p3", &p3}, {"p4", &p4},
		{"p5", &p5}, {"p6", &p6}, {"p7", &p7}, {"p8", &p8}, {"p9", &p9},
		{"instManaged", &instManaged},
		{"instTenantInSameProv", &instTenantInSameProv},
		{"instTenantInSameProv2", &instTenantInSameProv2},
		{"instTenantInOtherProv", &instTenantInOtherProv},
		{"instSameNameTenant1", &instSameNameTenant1},
		{"instSameNameTenant2Managed", &instSameNameTenant2Managed},
		{"instT2", &instT2},
		{"instNestedMeta", &instNestedMeta},
		{"instManagedProxy", &instManagedProxy},
		{"instManagedExactInstance", &instManagedExactInstance},
		{"instManagedExactInstances", &instManagedExactInstances},
		{"instUnmanagedCustom", &instUnmanagedCustom},
	}
	for _, f := range fixtures {
		if err := db.Create(f.model).Error; err != nil {
			t.Fatalf("failed to insert test fixture %s: %v", f.name, err)
		}
	}

	originalDB := dao.DB
	dao.DB = db
	defer func() {
		dao.DB = originalDB
	}()

	tests := []struct {
		name            string
		method          string
		url             string
		body            string
		tenantID        string
		expectedStatus  int
		expectBlocked   bool
		expectedQueries *int
	}{
		{
			name:           "Tenant mutating own instance in provider with managed instance must be ALLOWED (200)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI-API-Compatible/instances/uuid-tenant-inst-1",
			body:           `{"instance_name":"my-openai-key","model_info":[]}`,
			tenantID:       "tenant-1",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:            "Upstream batch drop route: Tenant deleting own instance in provider with managed instance must be ALLOWED (200)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-tenant-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusOK,
			expectBlocked:   false,
			expectedQueries: ptr(1),
		},
		{
			name:            "Upstream batch drop route: Tenant deleting managed instance must be BLOCKED (403)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-managed-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusForbidden,
			expectBlocked:   true,
			expectedQueries: ptr(1),
		},
		{
			name:           "Tenant mutating managed instance must be BLOCKED (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI-API-Compatible/instances/uuid-managed-inst-1",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant deleting provider that contains managed instance must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/prov-t1-openai",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant deleting custom provider without managed instance must be ALLOWED (200)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/prov-t1-local",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "Multi-tenant isolation: Tenant 2 deleting their own OpenAI-API-Compatible provider must be ALLOWED (200)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI-API-Compatible",
			tenantID:       "tenant-2",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "Multi-tenant isolation: Tenant 1 deleting OpenAI-API-Compatible provider (which has managed inst) must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI-API-Compatible",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant 1 deleting OpenAI provider via /v1/user/setting/model/provider?name=OpenAI-API-Compatible must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=OpenAI-API-Compatible",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant 1 deleting OpenAI provider via /api/v1/user/setting/model/provider?name=OpenAI-API-Compatible must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/user/setting/model/provider?name=OpenAI-API-Compatible",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant 1 deleting custom provider via /v1/user/setting/model/provider?name=prov-t1-local must be ALLOWED (200)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=prov-t1-local",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE on managed provider with unmanaged instance query parameter must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI-API-Compatible?instance=uuid-tenant-inst-1",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider via setting route with unmanaged instance query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/v1/user/setting/model/provider?name=OpenAI-API-Compatible&instance=uuid-tenant-inst-1",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "Tenant deleting provider with nested metadata is_managed:true (top-level is_managed:false) must be ALLOWED (200)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/prov-t1-nested",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE on managed provider named 'instance-proxy' with unmanaged instance query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instance-proxy?instance=nonexistent",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider named 'instance-proxy' without query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instance-proxy",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider named exactly 'instance' with unmanaged instance query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instance?instance=nonexistent",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider named exactly 'instance' without query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instance",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider named exactly 'instances' with unmanaged instance query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instances?instance=nonexistent",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on managed provider named exactly 'instances' without query param must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instances",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "DELETE on unmanaged provider named 'instance-custom' must be ALLOWED (200)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/instance-custom",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusOK,
			expectBlocked:  false,
		},
		{
			name:           "DELETE on batch drop route with polymorphic field in body targeting managed instance must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/providers/OpenAI-API-Compatible/instances?instance=nonexistent",
			body:           `{"instances":["uuid-managed-inst-1"],"name":42}`,
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:            "DELETE on batch drop route with undecodable malformed JSON body must fail closed and be BLOCKED (403)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances?instance=nonexistent",
			body:            `{"instances": [corrupt`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusForbidden,
			expectBlocked:   true,
			expectedQueries: ptr(0),
		},
		{
			name:            "DELETE on batch drop route with polymorphic field targeting tenant-owned instance must be ALLOWED (200)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-tenant-inst-1"],"name":42}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusOK,
			expectBlocked:   false,
			expectedQueries: ptr(1),
		},
		{
			name:            "DELETE on batch drop route with case-insensitive 'Instances' key targeting managed instance must be BLOCKED (403)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"Instances":["uuid-managed-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusForbidden,
			expectBlocked:   true,
			expectedQueries: ptr(1),
		},
		{
			name:            "DELETE on batch drop route with uppercase 'INSTANCES' key targeting managed instance must be BLOCKED (403)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"INSTANCES":["uuid-managed-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusForbidden,
			expectBlocked:   true,
			expectedQueries: ptr(1),
		},
		{
			name:            "DELETE with request body exceeding request body limit must be REJECTED (413)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["` + strings.Repeat("a", int(common.GetMaxJSONRequestBodyBytes())+10) + `"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusRequestEntityTooLarge,
			expectBlocked:   false,
			expectedQueries: ptr(0),
		},
		{
			name:            "DELETE on batch drop route exceeding MaxBatchInstanceTargets must be REJECTED (400)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":[` + strings.Repeat(`"uuid-tenant-inst-1",`, MaxBatchInstanceTargets) + `"uuid-tenant-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusBadRequest,
			expectBlocked:   false,
			expectedQueries: ptr(0),
		},
		{
			name:            "DELETE on batch drop route with duplicate case-insensitive 'instances' keys must be REJECTED (400)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-tenant-inst-1"],"Instances":["uuid-tenant-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusBadRequest,
			expectBlocked:   false,
			expectedQueries: ptr(0),
		},
		{
			name:            "DELETE on batch drop route with duplicate identical 'instances' keys must be REJECTED (400)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-tenant-inst-1"],"instances":["uuid-tenant-inst-1"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusBadRequest,
			expectBlocked:   false,
			expectedQueries: ptr(0),
		},
		{
			name:            "DELETE on batch drop route with multiple tenant instances executes exactly 1 coalesced query (200)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["uuid-tenant-inst-1","uuid-tenant-inst-3"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusOK,
			expectBlocked:   false,
			expectedQueries: ptr(1),
		},
		{
			name:            "DELETE on batch drop route with unmanaged instance sharing name with managed instance in other tenant must be ALLOWED (200)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/Custom-LocalAI/instances",
			body:            `{"instances":["shared-instance-name"]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusOK,
			expectBlocked:   false,
			expectedQueries: ptr(1),
		},
		{
			name:            "DELETE on batch drop route with padded managed instance identifier must be BLOCKED (403)",
			method:          http.MethodDelete,
			url:             "/api/v1/providers/OpenAI-API-Compatible/instances",
			body:            `{"instances":["  uuid-managed-inst-1  "]}`,
			tenantID:        "tenant-1",
			expectedStatus:  http.StatusForbidden,
			expectBlocked:   true,
			expectedQueries: ptr(1),
		},
		{
			name:           "DELETE on provider route with padded managed provider name query must be BLOCKED (403)",
			method:         http.MethodDelete,
			url:            "/api/v1/user/setting/model/provider?name=%20OpenAI-API-Compatible%20",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
		{
			name:           "PUT on instance route with padded managed instance parameter must be BLOCKED (403)",
			method:         http.MethodPut,
			url:            "/api/v1/providers/OpenAI-API-Compatible/instances/%20uuid-managed-inst-1%20",
			tenantID:       "tenant-1",
			expectedStatus: http.StatusForbidden,
			expectBlocked:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req, _ = http.NewRequest(tt.method, tt.url, bytes.NewBufferString(tt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, _ = http.NewRequest(tt.method, tt.url, nil)
			}

			w := httptest.NewRecorder()

			r := gin.New()
			// Simulate upstream AuthMiddleware setting tenant_id
			if tt.tenantID != "" {
				r.Use(func(c *gin.Context) {
					c.Set("tenant_id", tt.tenantID)
					c.Next()
				})
			}
			// Simulate RegisterEEProtectedRouter injecting ManagedProviderGuardMiddleware
			r.Use(ManagedProviderGuardMiddleware())

			providers := r.Group("/api/v1/providers")
			{
				providers.DELETE("/:provider_id_or_name", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"deleted": c.Param("provider_id_or_name")})
				})
				providers.PUT("/:provider_id_or_name/instances/:instance_id_or_name", func(c *gin.Context) {
					var req service.AlterProviderInstanceRequest
					if err := c.ShouldBindJSON(&req); err != nil {
						c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
						return
					}
					c.JSON(http.StatusOK, gin.H{"updated": c.Param("instance_id_or_name")})
				})
				providers.DELETE("/:provider_id_or_name/instances", func(c *gin.Context) {
					var dropReq struct {
						Instances []string `json:"instances"`
					}
					if err := c.ShouldBindJSON(&dropReq); err != nil {
						c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
						return
					}
					c.JSON(http.StatusOK, gin.H{"deleted_instances": dropReq.Instances})
				})
			}

			// Also register user setting and admin provider routes for database tests
			for _, prefix := range []string{"/v1/user/setting/model/provider", "/api/v1/user/setting/model/provider", "/api/v1/admin/model/provider"} {
				p := prefix
				r.DELETE(p, func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"deleted": "ok"})
				})
				r.DELETE(p+"/:provider_id_or_name", func(c *gin.Context) {
					c.JSON(http.StatusOK, gin.H{"deleted": c.Param("provider_id_or_name")})
				})
			}

			atomic.StoreInt64(&queryCount, 0)
			r.ServeHTTP(w, req)
			queriesExecuted := atomic.LoadInt64(&queryCount)

			if w.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d. Body: %s", tt.expectedStatus, w.Code, w.Body.String())
			}

			if tt.expectedQueries != nil {
				if queriesExecuted != int64(*tt.expectedQueries) {
					t.Fatalf("expected %d database queries, executed %d", *tt.expectedQueries, queriesExecuted)
				}
			}

			if tt.expectBlocked {
				var resp map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					t.Fatalf("failed to parse json response: %v", err)
				}
				if resp["code"] != float64(403) {
					t.Errorf("expected response code 403, got %v", resp["code"])
				}
				if resp["message"] != ErrMsgManagedProviderForbidden {
					t.Errorf("expected error message %q, got %q", ErrMsgManagedProviderForbidden, resp["message"])
				}
			}
		})
	}
}
