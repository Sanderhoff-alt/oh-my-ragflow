package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_DefaultConfigFallback(t *testing.T) {
	client := NewClient(GatewayConfig{})
	if client == nil {
		t.Fatal("expected non-nil client from zero-value config")
	}
}

func TestClient_GenerateKey(t *testing.T) {
	expectedMasterKey := "test-master-key-123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/key/generate" {
			t.Errorf("expected /key/generate, got %s", r.URL.Path)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer "+expectedMasterKey {
			t.Errorf("expected Bearer %s, got %s", expectedMasterKey, authHeader)
		}

		var req GenerateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.KeyAlias != "tenant-key-1" {
			t.Errorf("expected key_alias tenant-key-1, got %s", req.KeyAlias)
		}
		if len(req.Models) != 2 || req.Models[0] != "platform/chat" {
			t.Errorf("unexpected models: %v", req.Models)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(GenerateKeyResponse{
			Key:      "sk-litellm-virtual-key-xyz",
			KeyAlias: req.KeyAlias,
			KeyName:  req.KeyName,
			Models:   req.Models,
			UserID:   req.UserID,
		})
	}))
	defer server.Close()

	cfg := GatewayConfig{
		BaseURL:   server.URL,
		MasterKey: expectedMasterKey,
	}
	client := NewClient(cfg)

	// Test nil request validation error
	_, err := client.GenerateKey(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil generate request, got nil")
	}

	// Test empty models validation error
	_, err = client.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "tenant-key-1",
		Models:  []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty models in generate request, got nil")
	}

	resp, err := client.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "tenant-key-1",
		Models:  []string{"platform/chat", "platform/embedding"},
		UserID:  "tenant-uuid-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Key != "sk-litellm-virtual-key-xyz" {
		t.Errorf("expected key sk-litellm-virtual-key-xyz, got %s", resp.Key)
	}
	if resp.KeyName != "tenant-key-1" {
		t.Errorf("expected key_name tenant-key-1, got %s", resp.KeyName)
	}
}

func TestClient_ListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("expected GET /v1/models, got %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o-mini"},{"id":"deepseek-v3"}]}`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	names, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels failed: %v", err)
	}
	if len(names) != 2 || names[0] != "gpt-4o-mini" || names[1] != "deepseek-v3" {
		t.Errorf("unexpected model names: %v", names)
	}
}

func TestClient_ListModels_GatewayError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})
	if _, err := client.ListModels(context.Background()); err == nil {
		t.Fatal("expected error when gateway rejects the request")
	}
}

func TestClient_UpdateKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/update" {
			t.Errorf("expected /key/update, got %s", r.URL.Path)
		}

		var req UpdateKeyRequest
		json.NewDecoder(r.Body).Decode(&req)

		if req.Key != "sk-virtual-key-target" {
			t.Errorf("unexpected key: %s", req.Key)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(UpdateKeyResponse{
			Key:    req.Key,
			Models: req.Models,
		})
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	// Test nil request validation error
	_, err := client.UpdateKey(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil update request, got nil")
	}

	// Test validation error when key is empty
	_, err = client.UpdateKey(context.Background(), &UpdateKeyRequest{Key: ""})
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}

	// Test successful update
	resp, err := client.UpdateKey(context.Background(), &UpdateKeyRequest{
		Key:    "sk-virtual-key-target",
		Models: []string{"platform/chat"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Key != "sk-virtual-key-target" {
		t.Errorf("expected key sk-virtual-key-target, got %s", resp.Key)
	}
}

func TestClient_DeleteKeyByAlias(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/delete" {
			t.Errorf("expected /key/delete, got %s", r.URL.Path)
		}

		var payload map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&payload)

		if _, hasKeys := payload["keys"]; hasKeys {
			t.Errorf("payload must not carry raw keys, got %v", payload)
		}
		var aliases []string
		json.Unmarshal(payload["key_aliases"], &aliases)
		if len(aliases) != 1 || aliases[0] != "tenant_t1" {
			t.Errorf("unexpected key_aliases: %v", aliases)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"deleted_keys":["tenant_t1"]}`))
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	if err := client.DeleteKeyByAlias(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty alias, got nil")
	}

	if err := client.DeleteKeyByAlias(context.Background(), "tenant_t1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_DeleteKeyByAlias_NotFoundReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":{"error":"No keys found"}}`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	err := client.DeleteKeyByAlias(context.Background(), "tenant_missing")
	if err == nil {
		t.Fatal("expected error when gateway returns 404, got nil")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Errorf("expected error to carry 404 status, got %v", err)
	}
}

func TestClient_GetKeyInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/key/info") {
			t.Errorf("expected /key/info, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "sk-info-target" {
			t.Errorf("expected query param key=sk-info-target, got %s", r.URL.Query().Get("key"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(KeyInfoResponse{
			Key: "sk-info-target",
			Info: KeyInfo{
				KeyName: "info-test-key",
				Spend:   12.50,
				Models:  []string{"platform/chat"},
			},
		})
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	// Test empty key validation error
	_, err := client.GetKeyInfo(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}

	resp, err := client.GetKeyInfo(context.Background(), "sk-info-target")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Info.Spend != 12.50 {
		t.Errorf("expected spend 12.50, got %f", resp.Info.Spend)
	}
}

func TestClient_HealthCheck(t *testing.T) {
	status := http.StatusOK
	errorMsg := "service degraded"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		if status != http.StatusOK {
			w.Write([]byte(errorMsg))
		}
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	// Healthy check
	if err := client.HealthCheck(context.Background()); err != nil {
		t.Fatalf("expected healthy check, got error: %v", err)
	}

	// Unhealthy check
	status = http.StatusInternalServerError
	err := client.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected unhealthy error, got nil")
	}
	if !strings.Contains(err.Error(), errorMsg) {
		t.Errorf("expected error message to contain %q, got %q", errorMsg, err.Error())
	}
}

func TestClient_TimeoutControl(t *testing.T) {
	// Server hangs for 150ms
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Client configured with strict 30ms timeout
	cfg := GatewayConfig{
		BaseURL: server.URL,
		Timeout: 30 * time.Millisecond,
	}
	client := NewClient(cfg)

	err := client.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestClient_ErrorHandling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "model platform/invalid not found"}`))
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	_, err := client.GenerateKey(context.Background(), &GenerateKeyRequest{
		KeyName: "invalid-req",
		Models:  []string{"platform/chat"},
	})
	if err == nil {
		t.Fatal("expected error from 400 response, got nil")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Errorf("expected status 400 in error, got %v", err)
	}
}

func TestClient_URLSanitization(t *testing.T) {
	// Use an immediately closed test server to guarantee a deterministic connection error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	rawSecret := "sk-super-secret-token-12345678"
	_, err := client.GetKeyInfo(context.Background(), rawSecret)
	if err == nil {
		t.Fatal("expected network error, got nil")
	}

	if strings.Contains(err.Error(), rawSecret) {
		t.Fatalf("sensitive key leaked in transport error: %v", err)
	}
	if strings.Contains(err.Error(), "?") {
		t.Errorf("expected query parameters to be completely stripped from error URL, got: %v", err)
	}
}

func TestClient_GetSpendLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/spend/logs/v2") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		// Verify query parameters
		if r.URL.Query().Get("start_date") != "2026-11-15 00:00:00" {
			t.Errorf("unexpected start_date: %s", r.URL.Query().Get("start_date"))
		}
		if r.URL.Query().Get("end_date") != "2026-11-16 00:00:00" {
			t.Errorf("unexpected end_date: %s", r.URL.Query().Get("end_date"))
		}
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("unexpected page: %s", r.URL.Query().Get("page"))
		}
		if r.URL.Query().Get("page_size") != "100" {
			t.Errorf("unexpected page_size: %s", r.URL.Query().Get("page_size"))
		}

		resp := []SpendLogEntry{
			{
				RequestID:        "req-1",
				SpendID:          "spend-1",
				Model:            "gpt-4o",
				TotalTokens:      150,
				PromptTokens:     100,
				CompletionTokens: 50,
				Spend:            0.0025,
				StartTime:        "2026-11-15 10:00:00",
				Metadata: map[string]interface{}{
					"user_api_key_alias": "tenant_t1",
				},
			},
			{
				RequestID:        "req-2",
				SpendID:          "spend-2",
				Model:            "text-embedding-3-small",
				TotalTokens:      50,
				PromptTokens:     50,
				CompletionTokens: 0,
				Spend:            0.0001,
				StartTime:        "2026-11-15 11:00:00",
				Metadata: map[string]interface{}{
					"user_api_key_alias": "tenant_t2",
				},
			},
		}
		// Wrap in pinned LiteLLM v1.110.0 response envelope
		envelope := map[string]interface{}{
			"data":      resp,
			"total":     len(resp),
			"page":      1,
			"page_size": 100,
		}
		json.NewEncoder(w).Encode(envelope)
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	logs, err := client.GetSpendLogs(context.Background(), SpendLogQueryParams{
		StartDate: "2026-11-15 00:00:00",
		EndDate:   "2026-11-16 00:00:00",
		Page:      1,
		PageSize:  100,
	})
	if err != nil {
		t.Fatalf("failed to get spend logs: %v", err)
	}

	if len(logs) != 2 {
		t.Fatalf("expected 2 spend logs, got %d", len(logs))
	}
	if logs[0].SpendID != "spend-1" || logs[0].TotalTokens != 150 {
		t.Errorf("unexpected spend log 0: %+v", logs[0])
	}
	if logs[1].SpendID != "spend-2" || logs[1].TotalTokens != 50 {
		t.Errorf("unexpected spend log 1: %+v", logs[1])
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "****"},
		{"short", "****"},
		{"abcdefgh", "****"},
		{"sk-1234", "sk-****"},
		{"sk-123456", "sk-****"},
		{"sk-12345678", "sk-****"},
		{"sk-123456789", "sk-****"},
		{"sk-1234567890", "sk-1234...7890"},
		{"sk-1234567890123", "sk-1234...0123"},
		{"sk-litellm-virtual-key-secret-9999", "sk-lite...9999"},
		{"custom-api-token-long-12345678", "cus...678"},
	}

	for _, tt := range tests {
		masked := MaskKey(tt.input)
		if masked != tt.expected {
			t.Errorf("MaskKey(%q) = %q, want %q", tt.input, masked, tt.expected)
		}
	}
}
