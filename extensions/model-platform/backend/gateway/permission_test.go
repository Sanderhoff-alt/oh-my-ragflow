package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockLiteLLMProxy simulates the permission routing and management endpoint blocking of LiteLLM Proxy.
type mockLiteLLMProxy struct {
	masterKey string
	mu        sync.RWMutex
	keys      map[string]*virtualKeyRecord
}

type virtualKeyRecord struct {
	key       string
	models    map[string]bool
	maxBudget *float64
	spend     float64
	expires   time.Time
	isExpired bool
}

func newMockLiteLLMProxy(masterKey string) *mockLiteLLMProxy {
	return &mockLiteLLMProxy{
		masterKey: masterKey,
		keys:      make(map[string]*virtualKeyRecord),
	}
}

func (m *mockLiteLLMProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")

	path := r.URL.Path

	// 1. Management Endpoints: Strictly require Master Key!
	// Any virtual key attempting to access management endpoints must be rejected with 401/403.
	if strings.HasPrefix(path, "/key/") || strings.HasPrefix(path, "/model/") || strings.HasPrefix(path, "/team/") {
		if token != m.masterKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error": {"message": "Invalid master key. Virtual keys are not permitted to access management endpoints.", "code": 401}}`))
			return
		}

		m.handleManagementEndpoints(w, r)
		return
	}

	// 2. Health endpoint
	if path == "/health" || path == "/health/liveliness" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "healthy"}`))
		return
	}

	// 3. Inference Endpoints (/chat/completions, /embeddings)
	// Requires valid virtual key and checks model whitelist, expiry, and budget.
	m.handleInferenceEndpoints(w, r, token)
}

func (m *mockLiteLLMProxy) handleManagementEndpoints(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch r.URL.Path {
	case "/key/generate":
		var req GenerateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		generatedKey := "sk-litellm-vk-" + req.KeyName
		modelsMap := make(map[string]bool)
		for _, model := range req.Models {
			modelsMap[model] = true
		}
		record := &virtualKeyRecord{
			key:       generatedKey,
			models:    modelsMap,
			maxBudget: req.MaxBudget,
			spend:     0.0,
			expires:   time.Now().Add(24 * time.Hour),
		}
		m.keys[generatedKey] = record

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(GenerateKeyResponse{
			Key:     generatedKey,
			KeyName: req.KeyName,
			Models:  req.Models,
			UserID:  req.UserID,
		})

	case "/key/delete":
		var req DeleteKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deleted := make([]string, 0)
		for _, k := range req.Keys {
			if _, exists := m.keys[k]; exists {
				delete(m.keys, k)
				deleted = append(deleted, k)
			}
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(DeleteKeyResponse{DeletedKeys: deleted})

	case "/key/info":
		keyParam := r.URL.Query().Get("key")
		rec, exists := m.keys[keyParam]
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "key not found"}`))
			return
		}
		modelsList := make([]string, 0, len(rec.models))
		for mdl := range rec.models {
			modelsList = append(modelsList, mdl)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(KeyInfoResponse{
			Key: rec.key,
			Info: KeyInfo{
				KeyName:   rec.key,
				Spend:     rec.spend,
				MaxBudget: rec.maxBudget,
				Models:    modelsList,
			},
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *mockLiteLLMProxy) handleInferenceEndpoints(w http.ResponseWriter, r *http.Request, token string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")

	// Verify key exists
	rec, exists := m.keys[token]
	if !exists {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Invalid virtual key or key revoked.", "code": 401}}`))
		return
	}

	// Verify key not expired
	if rec.isExpired || time.Now().After(rec.expires) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": {"message": "Virtual key has expired.", "code": 401}}`))
		return
	}

	// Verify budget not exceeded
	if rec.maxBudget != nil && rec.spend >= *rec.maxBudget {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": {"message": "Virtual key budget exceeded.", "code": 403}}`))
		return
	}

	// Parse model from inference payload
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"message": "model parameter is required", "code": 400}}`))
		return
	}

	// Enforce Model Whitelist!
	if !rec.models[payload.Model] {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error": {"message": "Access to requested model is not allowed for this virtual key.", "code": "model_not_allowed"}}`))
		return
	}

	// Authorized inference call succeeded
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"id": "chatcmpl-mock-success", "choices": [{"message": {"role": "assistant", "content": "mock platform response"}}]}`))
}

func TestGatewayPermissions_ManagementEndpointBlocking(t *testing.T) {
	masterKey := "master-super-secret-key-999"
	proxy := newMockLiteLLMProxy(masterKey)
	server := httptest.NewServer(proxy)
	defer server.Close()

	// 1. Generate a tenant virtual key using Master Key
	masterClient := NewClient(GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: masterKey,
	})

	genResp, err := masterClient.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "tenant-a",
		Models:  []string{"platform/chat"},
	})
	if err != nil {
		t.Fatalf("failed to generate key with master key: %v", err)
	}

	tenantVirtualKey := genResp.Key
	if !strings.HasPrefix(tenantVirtualKey, "sk-") {
		t.Fatalf("expected sk- prefix, got %s", tenantVirtualKey)
	}

	// 2. Tenant Virtual Key attempts to call management endpoint (/key/generate)
	// Must be BLOCKED with 401 Unauthorized!
	tenantClient := NewClient(GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: tenantVirtualKey, // Tenant attempting to use their virtual key as master key
	})

	_, err = tenantClient.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "tenant-rogue-key",
		Models:  []string{"platform/chat"},
	})
	if err == nil {
		t.Fatal("expected virtual key to be blocked from /key/generate, but succeeded!")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}

	// 3. Tenant Virtual Key attempts to call /key/delete
	_, err = tenantClient.DeleteKey(context.Background(), []string{tenantVirtualKey})
	if err == nil {
		t.Fatal("expected virtual key to be blocked from /key/delete, but succeeded!")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}

func TestGatewayPermissions_ModelWhitelistEnforcement(t *testing.T) {
	masterKey := "master-key-whitelist-test"
	proxy := newMockLiteLLMProxy(masterKey)
	server := httptest.NewServer(proxy)
	defer server.Close()

	masterClient := NewClient(GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: masterKey,
	})

	// Generate virtual key with whitelist containing ONLY "platform/chat"
	genResp, err := masterClient.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "tenant-chat-only",
		Models:  []string{"platform/chat"},
	})
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	tenantKey := genResp.Key

	// Helper to send inference request
	sendInference := func(token string, modelName string) int {
		body := fmt.Sprintf(`{"model": "%s", "messages": [{"role": "user", "content": "hi"}]}`, modelName)
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("inference request error: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Case 1: Call authorized model "platform/chat" -> 200 OK
	status := sendInference(tenantKey, "platform/chat")
	if status != http.StatusOK {
		t.Errorf("expected 200 for authorized model, got %d", status)
	}

	// Case 2: Call unauthorized model "platform/embedding" -> 403 Forbidden
	status = sendInference(tenantKey, "platform/embedding")
	if status != http.StatusForbidden {
		t.Errorf("expected 403 for unauthorized model, got %d", status)
	}

	// Case 3: Call arbitrary unassigned model "gpt-4-unauthorized" -> 403 Forbidden
	status = sendInference(tenantKey, "gpt-4-unauthorized")
	if status != http.StatusForbidden {
		t.Errorf("expected 403 for unassigned model, got %d", status)
	}

	// Case 4: Invalid/revoked virtual key -> 401 Unauthorized
	status = sendInference("sk-invalid-key", "platform/chat")
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid key, got %d", status)
	}
}

func TestGatewayPermissions_BudgetAndExpiryEnforcement(t *testing.T) {
	masterKey := "master-key-budget-test"
	proxy := newMockLiteLLMProxy(masterKey)
	server := httptest.NewServer(proxy)
	defer server.Close()

	masterClient := NewClient(GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: masterKey,
	})

	budget := 10.0
	genResp, err := masterClient.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName:   "tenant-budgeted",
		Models:    []string{"platform/chat"},
		MaxBudget: &budget,
	})
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	tenantKey := genResp.Key

	sendInference := func(token string) int {
		body := `{"model": "platform/chat", "messages": [{"role": "user", "content": "hi"}]}`
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/chat/completions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("inference request failed: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// Within budget -> 200 OK
	if status := sendInference(tenantKey); status != http.StatusOK {
		t.Errorf("expected 200 within budget, got %d", status)
	}

	// Exceed budget
	proxy.mu.Lock()
	proxy.keys[tenantKey].spend = 10.0
	proxy.mu.Unlock()

	// Exceeded budget -> 403 Forbidden
	if status := sendInference(tenantKey); status != http.StatusForbidden {
		t.Errorf("expected 403 when budget exceeded, got %d", status)
	}

	// Expired key
	proxy.mu.Lock()
	proxy.keys[tenantKey].isExpired = true
	proxy.mu.Unlock()

	// Expired key -> 401 Unauthorized
	if status := sendInference(tenantKey); status != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired key, got %d", status)
	}
}
