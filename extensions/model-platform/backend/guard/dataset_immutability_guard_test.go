package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupGuardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:guard_immut_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	err = db.AutoMigrate(
		&rentity.Knowledgebase{},
		&rentity.Document{},
		&rentity.TenantModelProvider{},
		&rentity.TenantModel{},
		&rentity.UserTenant{},
	)
	if err != nil {
		t.Fatalf("failed to auto migrate: %v", err)
	}

	return db
}

func setupTestServer(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Simulate auth middleware setting tenant_id
	r.Use(func(c *gin.Context) {
		if tid := c.GetHeader("X-Tenant-ID"); tid != "" {
			c.Set("tenant_id", tid)
		}
		c.Next()
	})

	// Apply guard middleware
	r.Use(DatasetImmutabilityGuardMiddleware(db))

	// Mock upstream update handler
	updateHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "updated"})
	}

	createHandler := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "created"})
	}

	r.POST("/api/v1/datasets", createHandler)
	r.POST("/v1/datasets", createHandler)
	r.PUT("/api/v1/datasets/:dataset_id", updateHandler)
	r.PATCH("/v1/datasets/:dataset_id", updateHandler)
	r.PUT("/api/v1/datasets/:dataset_id/documents", updateHandler)
	r.PUT("/api/v1/datasets/:dataset_id/metadata/config", updateHandler)
	r.PUT("/api/v1/pipelines/:id", updateHandler)

	return r
}

func TestDatasetImmutabilityGuard_EmptyDataset_AllowsModelChange(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-empty-1"
	origModel := "old-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Empty KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   0,
		ChunkNum: 0,
		Status:   &statusValid,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "new-embd-model",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected empty dataset model change allowed (200), got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetImmutabilityGuard_DatasetWithChunks_BlocksModelChange(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-with-chunks"
	origModel := "orig-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Parsed KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   3,
		ChunkNum: 45,
		Status:   &statusValid,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "tampered-embd-model",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for dataset with chunks, got %d: %s", resp.Code, resp.Body.String())
	}

	var res map[string]interface{}
	_ = json.Unmarshal(resp.Body.Bytes(), &res)
	if res["message"] != ErrMsgDatasetEmbeddingImmutable {
		t.Errorf("unexpected error message: %v", res["message"])
	}
}

func TestDatasetImmutabilityGuard_DatasetWithDocs_BlocksModelChange(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-with-docs-only"
	origModel := "orig-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Doc KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   1,
		ChunkNum: 0,
		Status:   &statusValid,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"embedding_model": "different-embd-model",
	})
	req, _ := http.NewRequest(http.MethodPatch, "/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for dataset with docs, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetImmutabilityGuard_IdenticalModel_PassesThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-ident-model"
	origModel := "text-embedding-3-small@platform@OpenAI-API-Compatible"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Unchanged KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   10,
		ChunkNum: 100,
		Status:   &statusValid,
	})

	// Sending back the same model
	body, _ := json.Marshal(map[string]interface{}{
		"name":    "renamed-kb",
		"embd_id": origModel,
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("sending identical model should pass through (200), got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetImmutabilityGuard_NonEmbeddingUpdates_PassThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-non-embd-update"
	origModel := "orig-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Before KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   5,
		ChunkNum: 50,
		Status:   &statusValid,
	})

	// Only name and description update
	body, _ := json.Marshal(map[string]interface{}{
		"name":        "After KB",
		"description": "updated description",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200 for non-embedding update, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetImmutabilityGuard_SubResourcePaths_PassThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-subresource"

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "ignored",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID+"/documents", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("sub-resource routes should not be intercepted, got %d", resp.Code)
	}
}

func TestDatasetImmutabilityGuard_DocumentTableCountFallback(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-doc-fallback"
	origModel := "orig-embd-model"
	statusValid := "1"

	// Knowledgebase says doc_num = 0, but document table has 1 record
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Lagging KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   0,
		ChunkNum: 0,
		Status:   &statusValid,
	})

	docStatus := "1"
	docName := "doc.pdf"
	db.Create(&rentity.Document{
		ID:           "doc-1",
		KbID:         kbID,
		Name:         &docName,
		ParserConfig: rentity.JSONMap{},
		Status:       &docStatus,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "new-embd",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request due to document table count fallback, got %d", resp.Code)
	}
}

func TestDatasetImmutabilityGuard_SubResourceMetadataConfig_PassesThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-non-empty-sub"
	statusValid := "1"
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Non Empty KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   "orig-model",
		DocNum:   5,
		ChunkNum: 50,
		Status:   &statusValid,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "different-model",
	})
	// Sub-resource metadata/config must not be intercepted by root dataset immutability guard
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID+"/metadata/config", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected sub-resource /metadata/config to pass through (200), got %d", resp.Code)
	}
}

func TestDatasetImmutabilityGuard_NonDatasetRouteWithIDParam_PassesThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	pipelineID := "pipeline-123"
	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "some-model",
	})
	// Non-dataset route with :id param must not be intercepted
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/pipelines/"+pipelineID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected non-dataset route /pipelines/:id to pass through (200), got %d", resp.Code)
	}
}

func TestDatasetImmutabilityGuard_MultiFieldPayload_BypassPrevented(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-multi-field"
	origModel := "orig-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Multi Field KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   2,
		ChunkNum: 20,
		Status:   &statusValid,
	})

	// embd_id matches existing, but embedding_model attempts a silent override
	body, _ := json.Marshal(map[string]interface{}{
		"embd_id":         origModel,
		"embedding_model": "sneaky-tampered-model",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when one of the embedding fields differs, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetImmutabilityGuard_MultiFieldPayload_AllIdentical_PassesThrough(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-multi-field-identical"
	origModel := "orig-embd-model"
	statusValid := "1"

	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Multi Field Identical KB",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   origModel,
		DocNum:   2,
		ChunkNum: 20,
		Status:   &statusValid,
	})

	// Both fields match existing model
	body, _ := json.Marshal(map[string]interface{}{
		"embd_id":         origModel,
		"embedding_model": origModel,
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Errorf("expected 200 OK when all embedding fields match existing model, got %d", resp.Code)
	}
}

func TestDatasetEmbeddingGuard_Create_BlocksDeprecatedModel(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	tenantID := "tenant-create-guard"
	providerID := "prov-1"
	depModel := "dep-embd-model"

	db.Create(&rentity.TenantModelProvider{
		ID:           providerID,
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-dep-1",
		ProviderID: providerID,
		ModelName:  depModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true,"read_only":true}`,
	})

	// 1. Explicitly requested deprecated model -> 400 Bad Request
	body, _ := json.Marshal(map[string]interface{}{
		"name":            "New KB with Deprecated Model",
		"embedding_model": depModel,
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when creating KB with deprecated model, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetEmbeddingGuard_Create_BlocksDeprecatedDefaultModel(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	tenantID := "tenant-def-guard"
	providerID := "prov-def"
	depModel := "dep-default-embd"

	db.Create(&rentity.TenantModelProvider{
		ID:           providerID,
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-dep-def",
		ProviderID: providerID,
		ModelName:  depModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true}`,
	})
	// Tenant's default embedding model is set to the deprecated model!
	db.Create(&rentity.UserTenant{
		TenantID: tenantID,
		EmbdID:   depModel,
	})

	// Body does NOT pass embedding_model
	body, _ := json.Marshal(map[string]interface{}{
		"name": "New KB without explicit model",
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when tenant default model is deprecated, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetEmbeddingGuard_Create_AllowsActiveModel(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	tenantID := "tenant-active-guard"
	providerID := "prov-active"
	activeModel := "active-embd-model"

	db.Create(&rentity.TenantModelProvider{
		ID:           providerID,
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-active-1",
		ProviderID: providerID,
		ModelName:  activeModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{}`,
	})

	body, _ := json.Marshal(map[string]interface{}{
		"name":            "New KB with Active Model",
		"embedding_model": activeModel,
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when creating KB with active model, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetEmbeddingGuard_Update_EmptyDataset_BlocksDeprecatedModel(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	tenantID := "tenant-empty-update"
	providerID := "prov-empty-update"
	depModel := "dep-update-model"

	db.Create(&rentity.TenantModelProvider{
		ID:           providerID,
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-dep-up",
		ProviderID: providerID,
		ModelName:  depModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{"deprecated":true}`,
	})

	kbID := "kb-empty-dep-switch"
	statusValid := "1"
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Empty KB Attempting Deprecated Switch",
		TenantID: tenantID,
		EmbdID:   "initial-active-model",
		DocNum:   0,
		ChunkNum: 0,
		Status:   &statusValid,
	})

	// Attempting to switch empty dataset to deprecated model -> 400 Bad Request (Closing the empty KB bypass)
	body, _ := json.Marshal(map[string]interface{}{
		"embedding_model": depModel,
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tenantID)
	resp := httptest.NewRecorder()

	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when switching empty KB to deprecated model, got %d: %s", resp.Code, resp.Body.String())
	}
}

func TestDatasetEmbeddingGuard_DeprecatedModel_FormattedJSONWithSpaces(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	tenantID := "tenant-spaces-test"
	providerID := "prov-spaces-test"
	depModel := "dep-spaces-model"
	activeModel := "active-model-with-false-flag"

	db.Create(&rentity.TenantModelProvider{
		ID:           providerID,
		TenantID:     tenantID,
		ProviderName: "OpenAI-API-Compatible",
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-dep-spaces",
		ProviderID: providerID,
		ModelName:  depModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{ "deprecated" : true }`,
	})
	db.Create(&rentity.TenantModel{
		ID:         "tm-active-false",
		ProviderID: providerID,
		ModelName:  activeModel,
		ModelType:  int(rentity.ModelTypeEmbedding),
		Status:     "active",
		Extra:      `{ "deprecated" : false }`,
	})

	// 1. Creation with formatted JSON deprecated model should be rejected
	bodyDep, _ := json.Marshal(map[string]interface{}{
		"name":    "KB Dep",
		"embd_id": depModel,
	})
	reqDep, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(bodyDep))
	reqDep.Header.Set("Content-Type", "application/json")
	reqDep.Header.Set("X-Tenant-ID", tenantID)
	respDep := httptest.NewRecorder()
	r.ServeHTTP(respDep, reqDep)

	if respDep.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for model with spaced JSON deprecated flag, got %d: %s", respDep.Code, respDep.Body.String())
	}

	// 2. Creation with model where deprecated is false should succeed
	bodyActive, _ := json.Marshal(map[string]interface{}{
		"name":    "KB Active",
		"embd_id": activeModel,
	})
	reqActive, _ := http.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBuffer(bodyActive))
	reqActive.Header.Set("Content-Type", "application/json")
	reqActive.Header.Set("X-Tenant-ID", tenantID)
	respActive := httptest.NewRecorder()
	r.ServeHTTP(respActive, reqActive)

	if respActive.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for model with deprecated=false, got %d: %s", respActive.Code, respActive.Body.String())
	}
}

func TestDatasetImmutabilityGuard_DocumentQueryFailure_AbortsWithInternalServerError(t *testing.T) {
	db := setupGuardTestDB(t)
	r := setupTestServer(db)

	kbID := "kb-error-test-1"
	statusValid := "1"
	db.Create(&rentity.Knowledgebase{
		ID:       kbID,
		Name:     "Empty KB Doc Query Fail",
		TenantID: "tenant-1",
		UserID:   "user-1",
		EmbdID:   "initial-model",
		DocNum:   0,
		ChunkNum: 0,
		Status:   &statusValid,
	})

	// Drop document table to simulate database error during verification
	if err := db.Migrator().DropTable(&rentity.Document{}); err != nil {
		t.Fatalf("failed to drop document table: %v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"embd_id": "different-model",
	})
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/datasets/"+kbID, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	r.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 InternalServerError when document verification query fails, got %d: %s", resp.Code, resp.Body.String())
	}
}
