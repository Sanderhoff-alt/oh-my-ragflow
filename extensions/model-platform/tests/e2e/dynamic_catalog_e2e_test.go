package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	mpadmin "ragflow/extensions/model-platform/backend/admin"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/publisher"
	mpreconciler "ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// catalogEnv is a live LiteLLM gateway, an in-memory RAGFlow database seeded with the baseline catalog
// (gpt-4o-mini and text-embedding-3-small as defaults), the admin API and the convergence worker.
type catalogEnv struct {
	baseURL string
	db      *gorm.DB
	client  gateway.Client
	worker  *publisher.Worker
	router  *gin.Engine
}

func newCatalogEnv(t *testing.T) *catalogEnv {
	t.Helper()
	baseURL, masterKey := liveGateway(t)
	db := setupE2ETestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&rentity.Knowledgebase{}, &rentity.Chat{}); err != nil {
		t.Fatalf("failed to migrate the knowledge base and assistant tables: %v", err)
	}

	client := gateway.NewClient(gateway.GatewayConfig{BaseURL: baseURL, MasterKey: masterKey, Timeout: 10 * time.Second})
	worker := publisher.NewWorker(func() *gorm.DB { return db }, client,
		publisher.WithBackoffPolicy(mpreconciler.FastTestBackoffPolicy))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(router.Group("/admin"), client, db)
	return &catalogEnv{baseURL: baseURL, db: db, client: client, worker: worker, router: router}
}

// provisionTenant creates a tenant with one user and reconciles it against the live gateway.
func (e *catalogEnv) provisionTenant(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	tenantID := fmt.Sprintf("catalog-%s-%d", name, time.Now().UnixNano())
	userID := "user-" + tenantID
	valid := "1"
	e.db.Create(&rentity.Tenant{ID: tenantID, Name: &tenantID, Status: &valid})
	e.db.Create(&rentity.User{ID: userID, Nickname: userID, Status: &valid})
	e.db.Create(&rentity.UserTenant{ID: "ut-" + tenantID, UserID: userID, TenantID: tenantID})
	e.db.Create(&mpentity.TenantReconcileRecord{TenantID: tenantID, Status: mpentity.StatusPending, Version: 1})
	t.Cleanup(func() { _, _ = mpreconciler.RevokeTenantKeys(ctx, e.client, tenantID) })

	if err := mpreconciler.NewTenantReconciler(e.client, e.baseURL, nil).Reconcile(ctx, e.db, tenantID); err != nil {
		t.Fatalf("failed to provision tenant %s: %v", name, err)
	}
	return tenantID
}

func (e *catalogEnv) tenantKey(t *testing.T, tenantID string) string {
	t.Helper()
	var provider rentity.TenantModelProvider
	if err := e.db.Where("tenant_id = ?", tenantID).First(&provider).Error; err != nil {
		t.Fatalf("tenant %s has no provider: %v", tenantID, err)
	}
	var instance rentity.TenantModelInstance
	if err := e.db.Where("provider_id = ? AND instance_name = ?", provider.ID, mpreconciler.PlatformInstanceName).First(&instance).Error; err != nil {
		t.Fatalf("tenant %s has no managed instance: %v", tenantID, err)
	}
	return instance.APIKey
}

// allowed reports whether the live gateway lists the model in the key whitelist.
func (e *catalogEnv) allowed(t *testing.T, key, model string) bool {
	t.Helper()
	info, err := e.client.GetKeyInfo(context.Background(), key)
	if err != nil {
		t.Fatalf("failed to read the key from the gateway: %v", err)
	}
	for _, m := range info.Info.Models {
		if m == model {
			return true
		}
	}
	return false
}

func (e *catalogEnv) modelID(t *testing.T, name string) string {
	t.Helper()
	var model mpentity.PlatformManagedModel
	if err := e.db.Where("model_name = ?", name).First(&model).Error; err != nil {
		t.Fatalf("model %s is not in the catalog: %v", name, err)
	}
	return model.ID
}

// admin calls the model platform admin API in process.
func (e *catalogEnv) admin(t *testing.T, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(method, "/admin/model-platform"+path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// converge runs the worker until every item has been applied, and logs the items that did not succeed.
func (e *catalogEnv) converge(t *testing.T) {
	t.Helper()
	for i := 0; i < 3; i++ {
		e.worker.RunOnce(context.Background())
	}
	var open []mpentity.ModelPublishItem
	e.db.Where("status <> ?", mpentity.PublishItemSucceeded).Find(&open)
	for _, item := range open {
		t.Logf("item %s/%s is %s after %d attempts: %s", item.TaskID, item.TenantID, item.Status, item.RetryCount, item.LastError)
	}
}

func (e *catalogEnv) publish(t *testing.T, name string, modelType rentity.ModelType) {
	t.Helper()
	code, out := e.admin(t, http.MethodPost, "/models", map[string]interface{}{"model_name": name, "model_type": int(modelType)})
	if code != http.StatusAccepted {
		t.Fatalf("publishing %s answered %d: %v", name, code, out)
	}
	e.converge(t)
}

func embedStatus(t *testing.T, baseURL, key, model string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"model": model, "input": "ping"})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/v1/embeddings", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("embedding request failed: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// Scenario 2: the administrator onboards a model once and every Ready tenant receives it, without any
// tenant default changing.
func TestE2E_Catalog_OnboardingBroadcastsToEveryTenant(t *testing.T) {
	e := newCatalogEnv(t)
	first := e.provisionTenant(t, "one")
	second := e.provisionTenant(t, "two")

	e.publish(t, "gpt-4o", rentity.ModelTypeChat)

	for _, tenantID := range []string{first, second} {
		key := e.tenantKey(t, tenantID)
		if !e.allowed(t, key, "gpt-4o") || !e.allowed(t, key, "gpt-4o-mini") {
			t.Errorf("tenant %s: the key must allow the new model and keep the baseline one", tenantID)
		}
		if status := settledStatus(func() int { return chatStatus(t, e.baseURL, key, "gpt-4o") }, false); status == http.StatusForbidden {
			t.Errorf("tenant %s: the new model must be callable through the gateway, got HTTP %d", tenantID, status)
		}
		var rows int64
		e.db.Table("tenant_model").
			Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model.provider_id").
			Where("tenant_model_provider.tenant_id = ? AND tenant_model.model_name = ? AND tenant_model.status = ?", tenantID, "gpt-4o", "active").
			Count(&rows)
		if rows != 1 {
			t.Errorf("tenant %s: expected one active gpt-4o row, got %d", tenantID, rows)
		}
		var ut rentity.UserTenant
		e.db.Where("tenant_id = ?", tenantID).First(&ut)
		if ut.LLMID != "gpt-4o-mini@platform@OpenAI-API-Compatible" {
			t.Errorf("tenant %s: onboarding must not change the default model, got %q", tenantID, ut.LLMID)
		}
	}
}

// Scenario 3: the platform default cannot be retired until another model has been promoted.
func TestE2E_Catalog_DefaultRetirementGuard(t *testing.T) {
	e := newCatalogEnv(t)
	tenantID := e.provisionTenant(t, "guard")
	e.publish(t, "gpt-4o", rentity.ModelTypeChat)

	code, out := e.admin(t, http.MethodPost, "/models/"+e.modelID(t, "gpt-4o-mini")+"/retire", nil)
	if code != http.StatusBadRequest {
		t.Fatalf("retiring the platform default must be refused with 400, got %d: %v", code, out)
	}

	if code, out = e.admin(t, http.MethodPut, "/models/"+e.modelID(t, "gpt-4o")+"/default", nil); code != http.StatusAccepted {
		t.Fatalf("promoting another model answered %d: %v", code, out)
	}
	e.converge(t)
	var ut rentity.UserTenant
	e.db.Where("tenant_id = ?", tenantID).First(&ut)
	if ut.LLMID != "gpt-4o@platform@OpenAI-API-Compatible" {
		t.Errorf("a platform_default user must move to the new default, got %q", ut.LLMID)
	}

	if code, out = e.admin(t, http.MethodPost, "/models/"+e.modelID(t, "gpt-4o-mini")+"/retire", nil); code != http.StatusAccepted {
		t.Fatalf("retiring the former default must now succeed, got %d: %v", code, out)
	}
}

// Scenario 4: an embedding model owns a vector space. A tenant with knowledge bases keeps it read-only
// (deprecated, still authorized at the gateway), a tenant without them loses it at once, and the sweep
// tombstones the first one when its last knowledge base is gone.
func TestE2E_Catalog_EmbeddingThreeStageLifecycle(t *testing.T) {
	e := newCatalogEnv(t)
	used := e.provisionTenant(t, "used")
	idle := e.provisionTenant(t, "idle")
	const small, smallRef = "text-embedding-3-small", "text-embedding-3-small@platform@OpenAI-API-Compatible"

	e.db.Create(&rentity.Knowledgebase{ID: "kb-" + used, TenantID: used, Name: "legacy kb", EmbdID: smallRef})
	e.publish(t, "text-embedding-3-large", rentity.ModelTypeEmbedding)
	if code, out := e.admin(t, http.MethodPut, "/models/"+e.modelID(t, "text-embedding-3-large")+"/default", nil); code != http.StatusAccepted {
		t.Fatalf("promoting the new embedding model answered %d: %v", code, out)
	}
	e.converge(t)
	if code, out := e.admin(t, http.MethodPost, "/models/"+e.modelID(t, small)+"/retire", nil); code != http.StatusAccepted {
		t.Fatalf("retiring the former default answered %d: %v", code, out)
	}
	e.converge(t)

	usedKey, idleKey := e.tenantKey(t, used), e.tenantKey(t, idle)
	embed := func(key string) func() int {
		return func() int { return embedStatus(t, e.baseURL, key, small) }
	}
	if !e.allowed(t, usedKey, small) || settledStatus(embed(usedKey), false) == http.StatusForbidden {
		t.Error("stage 2: a tenant with knowledge bases must keep reading through the gateway")
	}
	if e.allowed(t, idleKey, small) || settledStatus(embed(idleKey), true) != http.StatusForbidden {
		t.Error("a tenant without knowledge bases must lose the model at the gateway at once")
	}
	var row rentity.TenantModel
	e.db.Table("tenant_model").
		Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model.provider_id").
		Where("tenant_model_provider.tenant_id = ? AND tenant_model.model_name = ?", used, small).
		Select("tenant_model.*").First(&row)
	if deprecated, tombstoned := mpreconciler.RetirementFlags(row.Extra); row.Status != "active" || !deprecated || tombstoned {
		t.Errorf("stage 2: expected an active, deprecated row, got %+v", row)
	}

	e.db.Model(&rentity.Knowledgebase{}).Where("id = ?", "kb-"+used).Update("status", "0")
	e.converge(t)

	if e.allowed(t, usedKey, small) || settledStatus(embed(usedKey), true) != http.StatusForbidden {
		t.Error("stage 3: once its knowledge bases are gone the sweep must cut the model at the gateway")
	}
	var ut rentity.UserTenant
	e.db.Where("tenant_id = ?", used).First(&ut)
	if ut.EmbdID != "text-embedding-3-large@platform@OpenAI-API-Compatible" {
		t.Errorf("a default must not point at a tombstoned model, got %q", ut.EmbdID)
	}
}

// Scenario 5: a stateless model has no reference wait. It is cut at the gateway at once, so an assistant
// bound to it fails fast with a 403, while the other models of the tenant keep working.
func TestE2E_Catalog_ChatTwoStageFailFast(t *testing.T) {
	e := newCatalogEnv(t)
	tenantID := e.provisionTenant(t, "chat")
	e.publish(t, "gpt-4o", rentity.ModelTypeChat)
	key := e.tenantKey(t, tenantID)
	e.db.Create(&rentity.Chat{ID: "dialog-" + tenantID, TenantID: tenantID, LLMID: "gpt-4o",
		LLMSetting: rentity.JSONMap{}, PromptConfig: rentity.JSONMap{}, KBIDs: rentity.JSONSlice{}})

	if status := chatStatus(t, e.baseURL, key, "gpt-4o"); status == http.StatusForbidden {
		t.Fatalf("the assistant must work before the retirement, got HTTP %d", status)
	}
	if code, out := e.admin(t, http.MethodPost, "/models/"+e.modelID(t, "gpt-4o")+"/retire", nil); code != http.StatusAccepted {
		t.Fatalf("retiring the chat model answered %d: %v", code, out)
	}
	e.converge(t)

	if status := settledStatus(func() int { return chatStatus(t, e.baseURL, key, "gpt-4o") }, true); status != http.StatusForbidden {
		t.Errorf("an assistant bound to a tombstoned model must fail with 403, got HTTP %d", status)
	}
	if status := chatStatus(t, e.baseURL, key, "gpt-4o-mini"); status == http.StatusForbidden {
		t.Errorf("the other models of the tenant must keep working, got HTTP %d", status)
	}
	var dialogs int64
	e.db.Model(&rentity.Chat{}).Where("id = ?", "dialog-"+tenantID).Count(&dialogs)
	if dialogs != 1 {
		t.Error("retiring a model must never delete the assistants that used it")
	}
}
