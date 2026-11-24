package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	mpreconciler "ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"
)

// liveGateway returns the live LiteLLM test cluster, or skips unless REQUIRE_LIVE_GATEWAY forbids skipping.
func liveGateway(t *testing.T) (baseURL, masterKey string) {
	t.Helper()
	baseURL = os.Getenv("LITELLM_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:4000"
	}
	masterKey = os.Getenv("LITELLM_TEST_MASTER_KEY")
	if masterKey == "" {
		masterKey = "sk-master-e2e-platform-test-key"
	}
	if !isGatewayLive(baseURL + "/health/liveliness") {
		if os.Getenv("REQUIRE_LIVE_GATEWAY") == "true" {
			t.Fatalf("FATAL: REQUIRE_LIVE_GATEWAY is true, but the live gateway is unreachable on %s", baseURL)
		}
		t.Skipf("LiteLLM live container not running on %s; skipping", baseURL)
	}
	return baseURL, masterKey
}

// chatStatus calls a chat completion with a virtual key and returns the HTTP status. The upstream behind the
// test route is a mock, so only the authorization outcome matters: 403 means the key may not use the model.
func chatStatus(t *testing.T, baseURL, key, model string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "ping"}},
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("chat completion request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		t.Logf("chat completion of %s answered 429: %s", model, raw)
	}
	return resp.StatusCode
}

// revokeAtCleanup revokes a key a test issued once the test ends, addressing it by its hash like the platform does.
func revokeAtCleanup(t *testing.T, client gateway.Client, key string) {
	t.Helper()
	t.Cleanup(func() { _ = client.DeleteKeysByHash(context.Background(), []string{gateway.HashKey(key)}) })
}

// generateKeyWithoutModels asks LiteLLM for a key and leaves the models field out, which the gateway client
// refuses to do, to show what an empty whitelist would mean.
func generateKeyWithoutModels(t *testing.T, baseURL, masterKey, alias string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"key_alias": alias})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/key/generate", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+masterKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("key generation failed: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Key == "" {
		t.Fatalf("unexpected key generation response (status %d): %v", resp.StatusCode, err)
	}
	return out.Key
}

// TestE2E_LiveGateway_EmptyCatalogColdStart is scenario 1 of #31 against a real LiteLLM: with an empty catalog
// a tenant still becomes Ready, and its key is generated with the deny-all placeholder.
func TestE2E_LiveGateway_EmptyCatalogColdStart(t *testing.T) {
	baseURL, masterKey := liveGateway(t)
	ctx := context.Background()
	db := setupE2ETestDB(t)
	if err := db.Where("1 = 1").Delete(&mpentity.PlatformManagedModel{}).Error; err != nil {
		t.Fatalf("failed to empty the catalog: %v", err)
	}

	tenantID := fmt.Sprintf("tenant-cold-%d", time.Now().UnixNano())
	valid := "1"
	name := "Cold start tenant"
	db.Create(&rentity.Tenant{ID: tenantID, Name: &name, Status: &valid})
	db.Create(&mpentity.TenantReconcileRecord{TenantID: tenantID, Status: mpentity.StatusPending, Version: 1})

	client := gateway.NewClient(gateway.GatewayConfig{BaseURL: baseURL, MasterKey: masterKey, Timeout: 10 * time.Second})
	t.Cleanup(func() { _, _ = mpreconciler.RevokeTenantKeys(ctx, client, tenantID) })

	if err := mpreconciler.NewTenantReconciler(client, baseURL, nil).Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("an empty catalog must not fail the tenant: %v", err)
	}
	record := mpentity.TenantReconcileRecord{}
	if err := db.Where("tenant_id = ?", tenantID).First(&record).Error; err != nil || record.Status != mpentity.StatusReady {
		t.Fatalf("expected the tenant to be Ready, got %+v (%v)", record, err)
	}

	var instance rentity.TenantModelInstance
	if err := db.Where("instance_name = ?", mpreconciler.PlatformInstanceName).Order("create_time DESC").First(&instance).Error; err != nil {
		t.Fatalf("the managed instance must exist for the fan-out worker: %v", err)
	}
	info, err := client.GetKeyInfo(ctx, instance.APIKey)
	if err != nil {
		t.Fatalf("failed to read the key from the gateway: %v", err)
	}
	if len(info.Info.Models) != 1 || info.Info.Models[0] != mpreconciler.RevokedModelPlaceholder {
		t.Errorf("expected only the deny-all placeholder, got %v", info.Info.Models)
	}
	if status := chatStatus(t, baseURL, instance.APIKey, "gpt-4o-mini"); status != http.StatusForbidden {
		t.Errorf("a key without managed models must be denied by the gateway, got HTTP %d", status)
	}
}

// TestE2E_LiveGateway_KeyWhitelistSemantics pins the LiteLLM behavior the catalog design relies on.
func TestE2E_LiveGateway_KeyWhitelistSemantics(t *testing.T) {
	baseURL, masterKey := liveGateway(t)
	ctx := context.Background()
	client := gateway.NewClient(gateway.GatewayConfig{BaseURL: baseURL, MasterKey: masterKey, Timeout: 10 * time.Second})
	suffix := time.Now().UnixNano()

	// A whitelist may name models that are not routed yet: the catalog onboards a name before its route exists.
	unroutedAlias := fmt.Sprintf("e2e-unrouted-%d", suffix)
	unrouted, err := client.GenerateKey(ctx, &gateway.GenerateKeyRequest{
		KeyAlias: unroutedAlias,
		Models:   []string{"a-model-without-a-route", "platform/chat"},
	})
	if err != nil {
		t.Fatalf("LiteLLM must accept model names that are not in model_list: %v", err)
	}
	revokeAtCleanup(t, client, unrouted.Key)

	// The deny-all placeholder really denies a routed model.
	placeholderAlias := fmt.Sprintf("e2e-placeholder-%d", suffix)
	denied, err := client.GenerateKey(ctx, &gateway.GenerateKeyRequest{
		KeyAlias: placeholderAlias,
		Models:   []string{mpreconciler.RevokedModelPlaceholder},
	})
	if err != nil {
		t.Fatalf("failed to generate the placeholder key: %v", err)
	}
	revokeAtCleanup(t, client, denied.Key)
	if status := chatStatus(t, baseURL, denied.Key, "gpt-4o-mini"); status != http.StatusForbidden {
		t.Errorf("the placeholder key must be denied, got HTTP %d", status)
	}

	// An empty whitelist is NOT deny-all: this is why the placeholder exists and the client refuses an empty list.
	openAlias := fmt.Sprintf("e2e-unrestricted-%d", suffix)
	openKey := generateKeyWithoutModels(t, baseURL, masterKey, openAlias)
	revokeAtCleanup(t, client, openKey)
	if status := chatStatus(t, baseURL, openKey, "gpt-4o-mini"); status == http.StatusForbidden || status == http.StatusUnauthorized {
		t.Errorf("a key without a models list is expected to be unrestricted, got HTTP %d", status)
	}
	if _, err := client.GenerateKey(ctx, &gateway.GenerateKeyRequest{KeyAlias: "never-created"}); err == nil {
		t.Error("the gateway client must refuse to generate a key without models")
	}

	// Updating the whitelist to a routed model grants access.
	if _, err := client.UpdateKey(ctx, &gateway.UpdateKeyRequest{Key: denied.Key, Models: []string{"gpt-4o-mini"}}); err != nil {
		t.Fatalf("failed to update the whitelist: %v", err)
	}
	// LiteLLM caches the key object, so a changed whitelist is visible after a short propagation delay.
	if status := settledStatus(func() int { return chatStatus(t, baseURL, denied.Key, "gpt-4o-mini") }, false); status == http.StatusForbidden {
		info, infoErr := client.GetKeyInfo(ctx, denied.Key)
		t.Errorf("a key whose whitelist names the model must be allowed within 15s, still HTTP %d (gateway view: %+v, %v)", status, info, infoErr)
	}
}

// settledStatus polls until the gateway answers 403 (wantForbidden) or anything else, or 15s pass, and returns
// the last status. It absorbs LiteLLM's key cache delay after a whitelist change.
func settledStatus(call func() int, wantForbidden bool) int {
	status := call()
	deadline := time.Now().Add(15 * time.Second)
	for (status == http.StatusForbidden) != wantForbidden && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		status = call()
	}
	return status
}
