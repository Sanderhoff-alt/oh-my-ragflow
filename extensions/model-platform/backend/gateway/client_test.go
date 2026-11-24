package gateway

import (
	"context"
	"encoding/json"
	"errors"
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

func TestClient_GenerateKey_CreatesTheTeamFirst(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/team/new":
			var req TeamRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.TeamID != "tenant-1" {
				t.Errorf("unexpected team: %+v", req)
			}
			_, _ = w.Write([]byte(`{}`))
		case "/key/generate":
			_, _ = w.Write([]byte(`{"key":"sk-1"}`))
		}
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})
	if _, err := client.GenerateKey(context.Background(), &GenerateKeyRequest{KeyAlias: "a", TeamID: "tenant-1", Models: []string{"m"}}); err != nil {
		t.Fatalf("GenerateKey failed: %v", err)
	}
	if len(calls) != 2 || calls[0] != "/team/new" || calls[1] != "/key/generate" {
		t.Errorf("expected the team to be created before the key, got %v", calls)
	}
}

func TestClient_GenerateKey_AcceptsAnExistingTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/team/new" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"{'error': 'Team id = tenant-1 already exists. Please use a different team id.'}"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"key":"sk-1"}`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})
	if _, err := client.GenerateKey(context.Background(), &GenerateKeyRequest{KeyAlias: "a", TeamID: "tenant-1", Models: []string{"m"}}); err != nil {
		t.Fatalf("an existing team must not fail key generation: %v", err)
	}
}

func TestClient_GenerateKey_TeamFailureStopsBeforeTheKey(t *testing.T) {
	keyRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key/generate" {
			keyRequested = true
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})
	if _, err := client.GenerateKey(context.Background(), &GenerateKeyRequest{KeyAlias: "a", TeamID: "tenant-1", Models: []string{"m"}}); err == nil {
		t.Fatal("expected the team failure to surface")
	}
	if keyRequested {
		t.Error("no key may be generated when its team could not be ensured")
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

func TestClient_DeleteKeysByHash(t *testing.T) {
	hashA := HashKey("sk-key-a")
	hashB := HashKey("sk-key-b")
	var received [][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/delete" {
			t.Errorf("expected /key/delete, got %s", r.URL.Path)
		}

		var payload map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&payload)
		if _, hasAliases := payload["key_aliases"]; hasAliases {
			t.Errorf("payload must address keys by hash only, got %v", payload)
		}
		var keys []string
		json.Unmarshal(payload["keys"], &keys)
		received = append(received, keys)

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string][]string{"deleted_keys": keys})
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	if err := client.DeleteKeysByHash(context.Background(), []string{"sk-raw-token"}); err == nil {
		t.Fatal("expected error for a raw key instead of its hash, got nil")
	}
	if len(received) != 0 {
		t.Fatalf("a raw key must never reach the gateway, got %v", received)
	}

	if err := client.DeleteKeysByHash(context.Background(), []string{hashA, hashB}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(received) != 1 || len(received[0]) != 2 || received[0][0] != hashA || received[0][1] != hashB {
		t.Errorf("expected one delete request carrying both hashes, got %v", received)
	}
	if err := client.DeleteKeysByHash(context.Background(), nil); err != nil || len(received) != 1 {
		t.Errorf("deleting nothing must not call the gateway, got %v (err=%v)", received, err)
	}
}

func TestClient_DeleteKeysByHash_NotFoundIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":{"error":"No keys found"}}`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	if err := client.DeleteKeysByHash(context.Background(), []string{HashKey("sk-gone")}); err != nil {
		t.Fatalf("an already deleted key must not be an error, got %v", err)
	}
}

func TestClient_DeleteKeysByHash_UnrelatedNotFoundIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`<html>404 page not found</html>`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	err := client.DeleteKeysByHash(context.Background(), []string{HashKey("sk-any")})
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusNotFound {
		t.Fatalf("a 404 that is not the gateway's 'No keys found' must not count as a revocation, got %v", err)
	}
}

func TestClient_DeleteKeysByHash_ServerErrorIsReported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	err := client.DeleteKeysByHash(context.Background(), []string{HashKey("sk-any")})
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusInternalServerError {
		t.Fatalf("expected a 500 StatusError, got %v", err)
	}
}

func TestClient_ListTeamKeys_FollowsPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/list" {
			t.Errorf("expected /key/list, got %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("team_id") != "t1" || q.Get("return_full_object") != "true" || q.Get("size") != "100" {
			t.Errorf("unexpected query: %v", q)
		}
		switch q.Get("page") {
		case "1":
			w.Write([]byte(`{"keys":[{"token":"h1","key_alias":"a1","team_id":"t1"}],"total_pages":2}`))
		case "2":
			w.Write([]byte(`{"keys":[{"token":"h2","team_id":"t1"}],"total_pages":2}`))
		default:
			t.Errorf("unexpected page %q", q.Get("page"))
		}
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	if _, err := client.ListTeamKeys(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty team id, got nil")
	}
	keys, err := client.ListTeamKeys(context.Background(), "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 || keys[0].Token != "h1" || keys[1].Token != "h2" {
		t.Errorf("expected both pages of keys, got %+v", keys)
	}
}

func TestClient_ListTeamKeys_EmptyTeam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"keys":[],"total_count":0,"current_page":1,"total_pages":0}`))
	}))
	defer server.Close()

	client := NewClient(GatewayConfig{BaseURL: server.URL})

	keys, err := client.ListTeamKeys(context.Background(), "t1")
	if err != nil || len(keys) != 0 {
		t.Fatalf("expected no keys and no error, got %+v, %v", keys, err)
	}
}

func TestHashKey_MatchesGatewayTokenFormat(t *testing.T) {
	// sha256("abc") from the FIPS 180-2 test vectors; LiteLLM stores exactly this digest as the key token.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashKey("abc"); got != want {
		t.Fatalf("HashKey = %s, want %s", got, want)
	}
	if !isKeyHash(want) || isKeyHash("sk-abc") || isKeyHash(want[:63]) {
		t.Fatal("isKeyHash must accept only a 64 character hex digest")
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
