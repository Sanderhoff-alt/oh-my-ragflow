package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mpdao "ragflow/extensions/model-platform/backend/dao"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	mpmigration "ragflow/extensions/model-platform/backend/migration"
	mpreconciler "ragflow/extensions/model-platform/backend/reconciler"
	"ragflow/extensions/model-platform/backend/testenv"
	"ragflow/internal/common"
	rentity "ragflow/internal/entity"
	"ragflow/internal/entity/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type mockGatewayState struct {
	mu           sync.Mutex
	virtualKeys  map[string]bool
	keyAliases   map[string]string
	chatCalls    int
	embedCalls   int
	lastChatAuth string
	lastEmbAuth  string
	spendLogs    []gateway.SpendLogEntry
}

func setupE2ETestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:e2e_core_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	ctx := context.Background()
	// Migrate core RAGFlow tables
	err = db.AutoMigrate(
		&rentity.Tenant{},
		&rentity.User{},
		&rentity.UserTenant{},
		&rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{},
		&rentity.TenantModel{},
		&rentity.Knowledgebase{},
		&rentity.Document{},
	)
	if err != nil {
		t.Fatalf("failed to auto migrate upstream entities: %v", err)
	}

	// Migrate Model Platform tables
	if err := mpmigration.MigrateReconcileSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}
	if err := mpmigration.MigratePreferenceSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate preference schema: %v", err)
	}
	if err := mpmigration.MigrateSpendLogSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate spend log schema: %v", err)
	}
	if err := mpmigration.MigrateUsageSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate usage schema: %v", err)
	}
	if err := mpmigration.MigratePublishSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := testenv.SeedPlatformTestModels(db); err != nil {
		t.Fatalf("failed to seed the baseline catalog: %v", err)
	}

	return db
}

func setupMockLiteLLMGateway(t *testing.T) (*httptest.Server, *mockGatewayState) {
	t.Helper()
	state := &mockGatewayState{
		virtualKeys: make(map[string]bool),
		keyAliases:  make(map[string]string),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Health check
		if path == "/health/liveliness" || path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"healthy"}`))
			return
		}

		// Team creation, which the gateway client does before it generates a key that names a team
		if path == "/team/new" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{}`))
			return
		}

		// Virtual Key generation
		if path == "/key/generate" && r.Method == http.MethodPost {
			var req gateway.GenerateKeyRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			vKey := fmt.Sprintf("sk-litellm-e2e-%s", req.TeamID)
			state.mu.Lock()
			state.virtualKeys[vKey] = true
			state.keyAliases[req.KeyName] = vKey
			state.mu.Unlock()

			json.NewEncoder(w).Encode(gateway.GenerateKeyResponse{
				Key:     vKey,
				KeyName: req.KeyName,
				Models:  req.Models,
				Spend:   0.0,
			})
			return
		}

		// Key delete
		if path == "/key/delete" && r.Method == http.MethodPost {
			var delReq gateway.DeleteKeyRequest
			_ = json.NewDecoder(r.Body).Decode(&delReq)
			state.mu.Lock()
			deleted := make([]string, 0, len(delReq.KeyAliases))
			for _, alias := range delReq.KeyAliases {
				if k, ok := state.keyAliases[alias]; ok {
					delete(state.virtualKeys, k)
					delete(state.keyAliases, alias)
					deleted = append(deleted, alias)
				}
			}
			state.mu.Unlock()
			if len(deleted) == 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"detail":{"error":"No keys found"}}`))
				return
			}
			json.NewEncoder(w).Encode(map[string][]string{"deleted_keys": deleted})
			return
		}

		// Key info
		if path == "/key/info" {
			key := r.URL.Query().Get("key")
			state.mu.Lock()
			isActive := state.virtualKeys[key]
			state.mu.Unlock()
			status := "active"
			if !isActive {
				status = "deleted"
			}
			json.NewEncoder(w).Encode(gateway.KeyInfoResponse{
				Key: key,
				Info: gateway.KeyInfo{
					KeyName: "test-key",
					Status:  status,
					Models:  []string{"gpt-4o-mini", "text-embedding-3-small"},
				},
			})
			return
		}

		// Spend logs endpoint (/spend/logs/v2) with pagination support
		if strings.HasPrefix(path, "/spend/logs") {
			state.mu.Lock()
			logsCopy := append([]gateway.SpendLogEntry(nil), state.spendLogs...)
			state.mu.Unlock()

			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
			if page <= 0 {
				page = 1
			}
			if pageSize <= 0 {
				pageSize = 100
			}

			start := (page - 1) * pageSize
			if start >= len(logsCopy) {
				json.NewEncoder(w).Encode([]gateway.SpendLogEntry{})
				return
			}
			end := start + pageSize
			if end > len(logsCopy) {
				end = len(logsCopy)
			}

			json.NewEncoder(w).Encode(logsCopy[start:end])
			return
		}

		// Validate virtual key bearer token for inference endpoints
		authHeader := r.Header.Get("Authorization")
		expectedKey := strings.TrimPrefix(authHeader, "Bearer ")
		state.mu.Lock()
		validKey := state.virtualKeys[expectedKey]
		state.mu.Unlock()

		if !validKey && !strings.HasPrefix(expectedKey, "sk-litellm-") {
			http.Error(w, `{"error":{"message":"Invalid or unauthorized virtual key","type":"auth_error"}}`, http.StatusUnauthorized)
			return
		}

		// Chat completions endpoint
		if strings.HasSuffix(path, "/chat/completions") {
			state.mu.Lock()
			state.chatCalls++
			state.lastChatAuth = authHeader
			state.mu.Unlock()

			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)

			// Check stream mode
			if isStream, ok := req["stream"].(bool); ok && isStream {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, _ := w.(http.Flusher)

				chunk1 := `data: {"id":"chatcmpl-stream","choices":[{"delta":{"content":"E2E "}}],"usage":null}` + "\n\n"
				w.Write([]byte(chunk1))
				if flusher != nil {
					flusher.Flush()
				}

				chunk2 := `data: {"id":"chatcmpl-stream","choices":[{"delta":{"content":"streaming OK."}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n"
				w.Write([]byte(chunk2))
				if flusher != nil {
					flusher.Flush()
				}

				w.Write([]byte("data: [DONE]\n\n"))
				return
			}

			// Non-streaming chat response
			resp := map[string]interface{}{
				"id":      "chatcmpl-e2e-sync",
				"object":  "chat.completion",
				"created": time.Now().Unix(),
				"model":   req["model"],
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"message": map[string]interface{}{
							"role":    "assistant",
							"content": "E2E end-to-end chat verification successful.",
						},
						"finish_reason": "stop",
					},
				},
				"usage": map[string]interface{}{
					"prompt_tokens":     15,
					"completion_tokens": 8,
					"total_tokens":      23,
				},
			}
			teamID := ""
			if strings.HasPrefix(expectedKey, "sk-litellm-e2e-") {
				teamID = strings.TrimPrefix(expectedKey, "sk-litellm-e2e-")
			}
			state.mu.Lock()
			state.spendLogs = append(state.spendLogs, gateway.SpendLogEntry{
				RequestID:        fmt.Sprintf("req-chat-%d", time.Now().UnixNano()),
				SpendID:          fmt.Sprintf("spend-chat-%d", time.Now().UnixNano()),
				APIKey:           expectedKey,
				TeamID:           teamID,
				Model:            fmt.Sprintf("%v", req["model"]),
				CallType:         "chat",
				Spend:            0.0002,
				TotalTokens:      23,
				PromptTokens:     15,
				CompletionTokens: 8,
				StartTime:        time.Now().UTC().Add(-1 * time.Second).Format("2006-01-02 15:04:05"),
				EndTime:          time.Now().UTC().Format("2006-01-02 15:04:05"),
			})
			state.mu.Unlock()

			json.NewEncoder(w).Encode(resp)
			return
		}

		// Embeddings endpoint
		if strings.HasSuffix(path, "/embeddings") {
			state.mu.Lock()
			state.embedCalls++
			state.lastEmbAuth = authHeader
			state.mu.Unlock()

			var req map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&req)

			// Generate 1536-dimensional mock embedding vector
			vecDim := 1536
			vector := make([]float64, vecDim)
			for i := 0; i < vecDim; i++ {
				vector[i] = float64(i) * 0.001
			}

			resp := map[string]interface{}{
				"object": "list",
				"model":  req["model"],
				"data": []map[string]interface{}{
					{
						"object":    "embedding",
						"index":     0,
						"embedding": vector,
					},
				},
				"usage": map[string]interface{}{
					"prompt_tokens": 5,
					"total_tokens":  5,
				},
			}
			teamID := ""
			if strings.HasPrefix(expectedKey, "sk-litellm-e2e-") {
				teamID = strings.TrimPrefix(expectedKey, "sk-litellm-e2e-")
			}
			state.mu.Lock()
			state.spendLogs = append(state.spendLogs, gateway.SpendLogEntry{
				RequestID:        fmt.Sprintf("req-emb-%d", time.Now().UnixNano()),
				SpendID:          fmt.Sprintf("spend-emb-%d", time.Now().UnixNano()),
				APIKey:           expectedKey,
				TeamID:           teamID,
				Model:            fmt.Sprintf("%v", req["model"]),
				CallType:         "embeddings",
				Spend:            0.00005,
				TotalTokens:      5,
				PromptTokens:     5,
				CompletionTokens: 0,
				StartTime:        time.Now().UTC().Add(-1 * time.Second).Format("2006-01-02 15:04:05"),
				EndTime:          time.Now().UTC().Format("2006-01-02 15:04:05"),
			})
			state.mu.Unlock()

			json.NewEncoder(w).Encode(resp)
			return
		}

		http.NotFound(w, r)
	}))

	return server, state
}

func TestE2E_EndToEnd_ProvisioningAndInference(t *testing.T) {
	db := setupE2ETestDB(t)
	server, state := setupMockLiteLLMGateway(t)
	defer server.Close()

	ctx := context.Background()
	tenantID := "tenant-e2e-core"
	userID := "user-e2e-core"

	// 1. Seed tenant and user in database
	statusValid := "1"
	tName := "E2E Tenant"
	db.Create(&rentity.Tenant{
		ID:     tenantID,
		Name:   &tName,
		Status: &statusValid,
	})
	db.Create(&rentity.User{
		ID:       userID,
		Nickname: "E2E User",
		Status:   &statusValid,
	})
	db.Create(&rentity.UserTenant{
		ID:       "ut-" + tenantID,
		UserID:   userID,
		TenantID: tenantID,
		Role:     "owner",
		Status:   &statusValid,
	})

	// Seed initial reconcile record
	reconcileDAO := mpdao.NewReconcileDAO()
	db.Create(&mpentity.TenantReconcileRecord{
		TenantID: tenantID,
		Status:   mpentity.StatusPending,
		Version:  1,
	})

	// 2. Initialize Reconciler with real LiteLLM Client pointing to mock gateway
	gwCfg := gateway.GatewayConfig{BaseURL: server.URL}
	gwClient := gateway.NewClient(gwCfg)

	reconciler := mpreconciler.NewTenantReconciler(
		gwClient,
		server.URL,
		[]string{"gpt-4o-mini", "text-embedding-3-small"},
	)

	// Execute reconciliation for tenant
	err := reconciler.Reconcile(ctx, db, tenantID)
	if err != nil {
		t.Fatalf("reconciler failed: %v", err)
	}

	// 3. Verify Reconciler provisioned all required entities in DB
	rec, err := reconcileDAO.GetByTenantID(ctx, db, tenantID)
	if err != nil || rec == nil || rec.Status != mpentity.StatusReady {
		t.Fatalf("expected reconcile record ready, got: %+v, err: %v", rec, err)
	}

	// Verify TenantModelProvider created
	var provider rentity.TenantModelProvider
	err = db.Where("tenant_id = ? AND provider_name = ?", tenantID, "OpenAI-API-Compatible").First(&provider).Error
	if err != nil {
		t.Fatalf("expected OpenAI-API-Compatible provider created: %v", err)
	}

	// Verify TenantModelInstance created with virtual key
	var instance rentity.TenantModelInstance
	err = db.Where("provider_id = ? AND instance_name = ?", provider.ID, "platform").First(&instance).Error
	if err != nil {
		t.Fatalf("expected platform instance created: %v", err)
	}
	if !strings.HasPrefix(instance.APIKey, "sk-litellm-e2e-") {
		t.Fatalf("expected virtual key in instance, got %s", instance.APIKey)
	}

	// Verify TenantModels created
	var chatModel, embModel rentity.TenantModel
	err = db.Where("instance_id = ? AND model_name = ?", instance.ID, "gpt-4o-mini").First(&chatModel).Error
	if err != nil {
		t.Fatalf("expected gpt-4o-mini model registered: %v", err)
	}
	err = db.Where("instance_id = ? AND model_name = ?", instance.ID, "text-embedding-3-small").First(&embModel).Error
	if err != nil {
		t.Fatalf("expected text-embedding-3-small model registered: %v", err)
	}

	// Verify UserTenant default model bindings
	var ut rentity.UserTenant
	db.Where("tenant_id = ? AND user_id = ?", tenantID, userID).First(&ut)
	if ut.LLMID != "gpt-4o-mini@platform@OpenAI-API-Compatible" {
		t.Fatalf("expected default LLMID bound, got: %v", ut.LLMID)
	}
	if ut.EmbdID != "text-embedding-3-small@platform@OpenAI-API-Compatible" {
		t.Fatalf("expected default EmbdID bound, got: %v", ut.EmbdID)
	}

	// 4. End-to-End Chat Generation with Native OpenAI-API-Compatible Driver
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
		{Role: "user", Content: "Hello model platform E2E"},
	}

	chatResp, err := driver.ChatWithMessages(ctx, "gpt-4o-mini", chatMessages, apiConfig, &models.ChatConfig{}, &chatUsage)
	if err != nil {
		t.Fatalf("ChatWithMessages failed: %v", err)
	}
	if chatResp == nil || chatResp.Answer == nil || !strings.Contains(*chatResp.Answer, "E2E end-to-end chat verification successful") {
		t.Fatalf("unexpected chat response content: %+v", chatResp)
	}
	if chatUsage.TotalTokens != 23 {
		t.Errorf("expected 23 tokens tracked in ModelUsage, got %d", chatUsage.TotalTokens)
	}

	// 5. End-to-End Embedding Generation with Native OpenAI-API-Compatible Driver
	var embUsage common.ModelUsage
	embModelName := "text-embedding-3-small"
	embReq := models.EmbedRequest{
		Texts: []string{"RAGFlow enterprise vector space", "knowledgebase search"},
	}

	embeddings, err := driver.Embed(ctx, &embModelName, embReq, apiConfig, nil, &embUsage)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(embeddings) != 1 {
		t.Fatalf("expected 1 embedding data item, got %d", len(embeddings))
	}
	if len(embeddings[0].Embedding) != 1536 {
		t.Fatalf("expected 1536-dimensional embedding vector, got %d", len(embeddings[0].Embedding))
	}

	// 6. Assert Gateway Invocations and Security Header Verification
	state.mu.Lock()
	defer state.mu.Unlock()

	if state.chatCalls != 1 {
		t.Errorf("expected 1 chat call to gateway, got %d", state.chatCalls)
	}
	if state.embedCalls != 1 {
		t.Errorf("expected 1 embed call to gateway, got %d", state.embedCalls)
	}
	if !strings.Contains(state.lastChatAuth, instance.APIKey) {
		t.Errorf("expected chat call authorized with virtual key %s, got %s", instance.APIKey, state.lastChatAuth)
	}
	if !strings.Contains(state.lastEmbAuth, instance.APIKey) {
		t.Errorf("expected embed call authorized with virtual key %s, got %s", instance.APIKey, state.lastEmbAuth)
	}
}

func TestE2E_EndToEnd_ChatStreamInference(t *testing.T) {
	server, state := setupMockLiteLLMGateway(t)
	defer server.Close()

	ctx := context.Background()
	urlSuffix := models.URLSuffix{
		Chat: "chat/completions",
	}
	driver := models.NewOpenAIAPICompatibleModel(map[string]string{}, urlSuffix)

	vKey := "sk-litellm-e2e-stream-key"
	state.mu.Lock()
	state.virtualKeys[vKey] = true
	state.mu.Unlock()

	apiConfig := &models.APIConfig{
		ApiKey:  &vKey,
		BaseURL: &server.URL,
	}

	var streamUsage common.ModelUsage
	chatMessages := []models.Message{
		{Role: "user", Content: "Stream test"},
	}

	var accumulatedContent string
	sender := func(content *string, reasoning *string) error {
		if content != nil && *content != "[DONE]" {
			accumulatedContent += *content
		}
		return nil
	}

	err := driver.ChatStreamlyWithSender(ctx, "gpt-4o-mini", chatMessages, apiConfig, &models.ChatConfig{}, &streamUsage, sender)
	if err != nil {
		t.Fatalf("ChatStreamlyWithSender failed: %v", err)
	}

	if accumulatedContent != "E2E streaming OK." {
		t.Fatalf("unexpected streaming content: %q", accumulatedContent)
	}
	if streamUsage.TotalTokens != 15 {
		t.Errorf("expected 15 stream tokens, got %d", streamUsage.TotalTokens)
	}
}

func TestE2E_EndToEnd_UnauthorizedKeyRejection(t *testing.T) {
	server, _ := setupMockLiteLLMGateway(t)
	defer server.Close()

	ctx := context.Background()
	urlSuffix := models.URLSuffix{
		Chat: "chat/completions",
	}
	driver := models.NewOpenAIAPICompatibleModel(map[string]string{}, urlSuffix)

	unauthorizedKey := "sk-unauthorized-random-token"
	apiConfig := &models.APIConfig{
		ApiKey:  &unauthorizedKey,
		BaseURL: &server.URL,
	}

	var chatUsage common.ModelUsage
	chatMessages := []models.Message{
		{Role: "user", Content: "Unauthorized check"},
	}

	_, err := driver.ChatWithMessages(ctx, "gpt-4o-mini", chatMessages, apiConfig, &models.ChatConfig{}, &chatUsage)
	if err == nil {
		t.Fatal("expected error on unauthorized key, got nil")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("expected 401 Unauthorized in error, got %v", err)
	}
}

func isGatewayLive(url string) bool {
	client := http.Client{Timeout: 1 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// TestE2E_LiveGatewayWithPostgreSQL performs real live end-to-end integration
// against the running LiteLLM + PostgreSQL Docker cluster from docker-compose.test.yml.
func TestE2E_LiveGatewayWithPostgreSQL(t *testing.T) {
	liveGatewayURL := "http://127.0.0.1:4000"
	strictLive := os.Getenv("REQUIRE_LIVE_GATEWAY") == "true"
	if !isGatewayLive(liveGatewayURL + "/health/liveliness") {
		if strictLive {
			t.Fatalf("FATAL: REQUIRE_LIVE_GATEWAY is true, but live gateway container is unreachable on %s", liveGatewayURL)
		}
		t.Skip("LiteLLM live container not running on 127.0.0.1:4000; skipping live container test")
	}

	db := setupE2ETestDB(t)
	ctx := context.Background()
	tenantID := "tenant-live-pg-e2e"
	userID := "user-live-pg-e2e"

	statusValid := "1"
	tName := "Live PG Tenant"
	db.Create(&rentity.Tenant{ID: tenantID, Name: &tName, Status: &statusValid})
	db.Create(&rentity.User{ID: userID, Nickname: "Live PG User", Status: &statusValid})
	db.Create(&rentity.UserTenant{UserID: userID, TenantID: tenantID})

	gwClient := gateway.NewClient(gateway.GatewayConfig{
		BaseURL:   liveGatewayURL,
		MasterKey: "sk-master-e2e-platform-test-key",
		Timeout:   5 * time.Second,
	})

	// 1. Verify healthcheck on live container
	if err := gwClient.HealthCheck(ctx); err != nil {
		t.Fatalf("live gateway healthcheck failed: %v", err)
	}

	// 2. Run Reconciler against real LiteLLM connected to PostgreSQL
	reconciler := mpreconciler.NewTenantReconciler(
		gwClient,
		liveGatewayURL,
		[]string{"gpt-4o-mini", "text-embedding-3-small"},
	)

	reconcileDAO := mpdao.NewReconcileDAO()
	db.Create(&mpentity.TenantReconcileRecord{
		TenantID: tenantID,
		Status:   mpentity.StatusPending,
		Version:  1,
	})

	if err := reconciler.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("reconcile against live LiteLLM failed: %v", err)
	}

	// 3. Verify record transitioned to READY
	rec, err := reconcileDAO.GetByTenantID(ctx, db, tenantID)
	if err != nil || rec.Status != mpentity.StatusReady {
		t.Fatalf("expected status ready after live reconcile, got: %+v, err: %v", rec, err)
	}

	// 4. Retrieve the issued virtual key from TenantModelInstance
	var inst rentity.TenantModelInstance
	if err := db.Where("instance_name = ?", "platform").First(&inst).Error; err != nil {
		t.Fatalf("failed to query managed instance: %v", err)
	}
	if inst.APIKey == "" {
		t.Fatal("expected non-empty virtual key in managed instance")
	}

	// 5. Query key info directly from LiteLLM / PostgreSQL
	info, err := gwClient.GetKeyInfo(ctx, inst.APIKey)
	if err != nil {
		t.Fatalf("failed to retrieve key info from live LiteLLM: %v", err)
	}
	if info.Info.TeamID != tenantID {
		t.Fatalf("expected team_id=%s in Postgres, got %s", tenantID, info.Info.TeamID)
	}

	// 6. Delete key from live LiteLLM by alias and verify physical cleanup
	keyAlias := "tenant_" + tenantID
	if err := gwClient.DeleteKeyByAlias(ctx, keyAlias); err != nil {
		t.Fatalf("failed to delete key by alias from live gateway: %v", err)
	}

	// 7. Verify key is no longer active in live LiteLLM
	delInfo, err := gwClient.GetKeyInfo(ctx, inst.APIKey)
	if err == nil && delInfo != nil && delInfo.Info.Status != "deleted" {
		t.Fatalf("expected deleted virtual key status to be 'deleted' or rejected, got status=%q", delInfo.Info.Status)
	}

	// 8. Alias is gone: a second revocation reports the key as not found
	if err := gwClient.DeleteKeyByAlias(ctx, keyAlias); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("expected 404 when deleting an already revoked alias, got: %v", err)
	}
}
