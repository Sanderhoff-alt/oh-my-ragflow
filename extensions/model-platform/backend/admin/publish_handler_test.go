package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"
)

type routedGatewayClient struct {
	mockAdminGatewayClient
	routed []string
}

func (m *routedGatewayClient) ListModels(ctx context.Context) ([]string, error) {
	return m.routed, nil
}

func setupPublishAdminServer(t *testing.T, routed ...string) (*gin.Engine, *gorm.DB) {
	t.Helper()
	t.Setenv("LITELLM_BASE_URL", "")
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:admin_publish_test_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	ctx := context.Background()
	if err := migration.MigrateReconcileSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}
	if err := migration.MigratePublishSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := db.AutoMigrate(&rentity.TenantModelProvider{}, &rentity.TenantModelInstance{}, &rentity.TenantModel{},
		&rentity.Knowledgebase{}, &rentity.Chat{}); err != nil {
		t.Fatalf("failed to migrate tenant model tables: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var client *routedGatewayClient
	if len(routed) > 0 {
		client = &routedGatewayClient{routed: routed}
		RegisterModelPlatformAdminRoutesWithGateway(r.Group("/admin"), client, db)
	} else {
		RegisterModelPlatformAdminRoutesWithGateway(r.Group("/admin"), nil, db)
	}
	return r, db
}

func doJSON(r *gin.Engine, method, path, body string) (int, map[string]interface{}) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestPublishAPI_RetireAndReactivate(t *testing.T) {
	r, db := setupPublishAdminServer(t, "deepseek-v3")
	db.Create(&entity.PlatformManagedModel{ID: "m-chat", ModelName: "deepseek-v3", ModelType: 1, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m-default", ModelName: "gpt-4o-mini", ModelType: 1, IsDefault: true, Status: entity.ManagedModelStatusActive})

	code, out := doJSON(r, http.MethodPost, "/admin/model-platform/models/m-chat/retire", "")
	if code != http.StatusAccepted || out["data"].(map[string]interface{})["task_id"] == "" {
		t.Fatalf("expected 202 with a task, got %d: %v", code, out)
	}
	var model entity.PlatformManagedModel
	db.Where("id = ?", "m-chat").First(&model)
	if model.Status != entity.ManagedModelStatusRetired {
		t.Errorf("expected the catalog to be retired, got %s", model.Status)
	}

	code, out = doJSON(r, http.MethodPost, "/admin/model-platform/models/m-default/retire", "")
	if code != http.StatusBadRequest || out["message"] == "" {
		t.Errorf("expected 400 for the platform default, got %d: %v", code, out)
	}
	if code, _ = doJSON(r, http.MethodPost, "/admin/model-platform/models/missing/retire", ""); code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown model, got %d", code)
	}

	if code, _ = doJSON(r, http.MethodPost, "/admin/model-platform/models/m-chat/reactivate", ""); code != http.StatusAccepted {
		t.Errorf("expected 202 on reactivation, got %d", code)
	}
	db.Where("id = ?", "m-chat").First(&model)
	if model.Status != entity.ManagedModelStatusActive {
		t.Errorf("expected the catalog to be active again, got %s", model.Status)
	}
}

func TestPublishAPI_ListCatalog(t *testing.T) {
	r, db := setupPublishAdminServer(t, "deepseek-v3")
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: 1, IsDefault: true, Status: entity.ManagedModelStatusActive})
	db.Create(&entity.PlatformManagedModel{ID: "m2", ModelName: "bge-m3", ModelType: 2, Status: entity.ManagedModelStatusRetired})

	code, out := doJSON(r, http.MethodGet, "/admin/model-platform/models", "")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, out)
	}
	items := out["data"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 catalog entries, got %v", items)
	}
	first := items[0].(map[string]interface{})
	if first["model_name"] != "bge-m3" || first["status"] != "retired" {
		t.Errorf("expected entries sorted by name with the derived status, got %v", first)
	}
	if _, ok := first["tenants"].(map[string]interface{}); !ok {
		t.Errorf("expected the per-tenant state breakdown, got %v", first)
	}
}

func TestPublishAPI_PublishAndProgress(t *testing.T) {
	r, db := setupPublishAdminServer(t, "deepseek-v3")
	db.Create(&entity.TenantReconcileRecord{TenantID: "t1", Status: entity.StatusReady, MaxRetries: 50, Version: 1})

	code, out := doJSON(r, http.MethodPost, "/admin/model-platform/models", `{"model_name":"deepseek-v3","model_type":1}`)
	if code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %v", code, out)
	}
	data := out["data"].(map[string]interface{})
	taskID := data["task_id"].(string)
	if taskID == "" || data["total"].(float64) != 1 {
		t.Fatalf("unexpected publish result: %v", data)
	}

	code, out = doJSON(r, http.MethodGet, "/admin/model-platform/models/publish/tasks/"+taskID, "")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", code, out)
	}
	progress := out["data"].(map[string]interface{})["progress"].(map[string]interface{})
	if progress["status"] != "running" || progress["total"].(float64) != 1 || progress["pending"].(float64) != 1 {
		t.Errorf("unexpected progress: %v", progress)
	}

	if code, _ = doJSON(r, http.MethodGet, "/admin/model-platform/models/publish/tasks/missing", ""); code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown task, got %d", code)
	}
}

func TestPublishAPI_ErrorMapping(t *testing.T) {
	r, db := setupPublishAdminServer(t, "deepseek-v3")
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: 1, Status: entity.ManagedModelStatusRetired})

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed body", `{`, http.StatusBadRequest},
		{"invalid model type", `{"model_name":"deepseek-v3","model_type":99}`, http.StatusBadRequest},
		{"route missing at gateway", `{"model_name":"unknown-model","model_type":1}`, http.StatusUnprocessableEntity},
		{"disabled catalog entry", `{"model_name":"deepseek-v3","model_type":1}`, http.StatusConflict},
	}
	for _, tc := range cases {
		if code, out := doJSON(r, http.MethodPost, "/admin/model-platform/models", tc.body); code != tc.want {
			t.Errorf("%s: expected %d, got %d: %v", tc.name, tc.want, code, out)
		}
	}
}

func TestPublishAPI_UnavailableWithoutGateway(t *testing.T) {
	r, _ := setupPublishAdminServer(t)
	if code, _ := doJSON(r, http.MethodPost, "/admin/model-platform/models", `{"model_name":"deepseek-v3","model_type":1}`); code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when no gateway is configured, got %d", code)
	}
}

func TestPublishAPI_RetryFailedItems(t *testing.T) {
	r, db := setupPublishAdminServer(t, "deepseek-v3")
	db.Create(&entity.ModelPublishTask{ID: "task1", ModelName: "deepseek-v3", ModelType: 1})
	db.Create(&entity.ModelPublishItem{TaskID: "task1", TenantID: "t1", Status: entity.PublishItemFailed, RetryCount: 20, LastError: "boom", Version: 3})

	code, out := doJSON(r, http.MethodGet, "/admin/model-platform/models/publish/tasks/task1", "")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	failed := out["data"].(map[string]interface{})["failed_items"].(map[string]interface{})
	if failed["total"].(float64) != 1 {
		t.Errorf("expected one failed tenant, got %v", failed)
	}

	code, out = doJSON(r, http.MethodPost, "/admin/model-platform/models/publish/tasks/task1/retry", "")
	if code != http.StatusOK || out["data"].(map[string]interface{})["retried"].(float64) != 1 {
		t.Fatalf("unexpected retry response %d: %v", code, out)
	}
	var item entity.ModelPublishItem
	db.Where("task_id = ? AND tenant_id = ?", "task1", "t1").First(&item)
	if item.Status != entity.PublishItemPending || item.RetryCount != 0 {
		t.Errorf("expected the item to be reset, got %+v", item)
	}

	if code, _ = doJSON(r, http.MethodPost, "/admin/model-platform/models/publish/tasks/missing/retry", ""); code != http.StatusNotFound {
		t.Errorf("expected 404 for an unknown task, got %d", code)
	}
}
