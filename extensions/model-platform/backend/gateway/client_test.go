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

func TestClient_DeleteKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key/delete" {
			t.Errorf("expected /key/delete, got %s", r.URL.Path)
		}

		var req DeleteKeyRequest
		json.NewDecoder(r.Body).Decode(&req)

		if len(req.Keys) != 1 || req.Keys[0] != "sk-to-delete" {
			t.Errorf("unexpected keys: %v", req.Keys)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(DeleteKeyResponse{
			DeletedKeys: req.Keys,
		})
	}))
	defer server.Close()

	cfg := GatewayConfig{BaseURL: server.URL}
	client := NewClient(cfg)

	// Test validation error when keys empty
	_, err := client.DeleteKey(context.Background(), []string{})
	if err == nil {
		t.Fatal("expected error for empty keys list, got nil")
	}

	resp, err := client.DeleteKey(context.Background(), []string{"sk-to-delete"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.DeletedKeys) != 1 || resp.DeletedKeys[0] != "sk-to-delete" {
		t.Errorf("unexpected deleted keys: %v", resp.DeletedKeys)
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
