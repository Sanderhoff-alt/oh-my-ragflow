package e2e_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	mpadmin "ragflow/extensions/model-platform/backend/admin"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	mpmetering "ragflow/extensions/model-platform/backend/metering"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func managedInstanceKey(t *testing.T, db *gorm.DB, tenantID string) string {
	t.Helper()
	var instance rentity.TenantModelInstance
	err := db.Where("instance_name = ?", "platform").
		Where("provider_id IN (?)", db.Model(&rentity.TenantModelProvider{}).Select("id").Where("tenant_id = ?", tenantID)).
		First(&instance).Error
	if err != nil {
		t.Fatalf("no managed instance for %s: %v", tenantID, err)
	}
	return instance.APIKey
}

// authorized reports whether the gateway accepted the key. The upstream behind the test route is a mock, so
// only a rejection (401 for an unknown or revoked key, 403 for a denied model) is meaningful.
func authorized(status int) bool {
	return status != http.StatusUnauthorized && status != http.StatusForbidden
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// TestE2E_KeyRotation_ReplacesTheKeyAtTheLiveGateway rotates a tenant key through the admin API against a
// real LiteLLM: the tenant switches keys without a gap, the old key is rejected once the gateway auth cache
// expires, the model whitelist is kept, and the spend of both keys lands in one usage row.
func TestE2E_KeyRotation_ReplacesTheKeyAtTheLiveGateway(t *testing.T) {
	env := newCatalogEnv(t)
	ctx := context.Background()
	tenantID := env.provisionTenant(t, "rotate")
	oldKey := managedInstanceKey(t, env.db, tenantID)

	if status := chatStatus(t, env.baseURL, oldKey, "gpt-4o-mini"); !authorized(status) {
		t.Fatalf("the provisioned key must work before the rotation, got HTTP %d", status)
	}
	oldInfo, err := env.client.GetKeyInfo(ctx, oldKey)
	if err != nil {
		t.Fatalf("failed to read the old key: %v", err)
	}

	router := gin.New()
	group := router.Group("/admin")
	group.Use(func(c *gin.Context) { c.Set("user_id", "e2e-admin") })
	mpadmin.RegisterModelPlatformAdminRoutesWithGateway(group, env.client, env.db)
	req := httptest.NewRequest(http.MethodPost, "/admin/model-platform/tenants/"+tenantID+"/rotate-key", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	newKey := managedInstanceKey(t, env.db, tenantID)
	if newKey == oldKey {
		t.Fatal("the managed instance must hold a new key")
	}
	if status := chatStatus(t, env.baseURL, newKey, "gpt-4o-mini"); !authorized(status) {
		t.Fatalf("the new key must work right after the rotation, got HTTP %d", status)
	}

	// The gateway workers cache keys for user_api_key_cache_ttl seconds, so the revocation is bounded, not instant.
	deadline := time.Now().Add(30 * time.Second)
	for authorized(chatStatus(t, env.baseURL, oldKey, "gpt-4o-mini")) {
		if time.Now().After(deadline) {
			t.Fatal("the old key was still accepted 30s after the rotation")
		}
		time.Sleep(200 * time.Millisecond)
	}

	keys, err := env.client.ListTeamKeys(ctx, tenantID)
	if err != nil || len(keys) != 1 || keys[0].Token != gateway.HashKey(newKey) {
		t.Fatalf("the team must hold exactly the new key, got %+v (err=%v)", keys, err)
	}
	newInfo, err := env.client.GetKeyInfo(ctx, newKey)
	if err != nil {
		t.Fatalf("failed to read the new key: %v", err)
	}
	if strings.Join(sortedCopy(newInfo.Info.Models), ",") != strings.Join(sortedCopy(oldInfo.Info.Models), ",") {
		t.Errorf("the whitelist must survive the rotation, was %v, now %v", oldInfo.Info.Models, newInfo.Info.Models)
	}
	if newInfo.Info.TeamID != tenantID {
		t.Errorf("the new key must belong to the tenant team, got %q", newInfo.Info.TeamID)
	}

	assertSpendOfBothKeysIsOneUsageRow(t, env, tenantID, oldKey, newKey)
}

func assertSpendOfBothKeysIsOneUsageRow(t *testing.T, env *catalogEnv, tenantID, oldKey, newKey string) {
	t.Helper()
	ctx := context.Background()
	collector := mpmetering.NewSpendLogCollector(env.db, env.client)
	from, to := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)

	var logs []mpentity.GatewaySpendLog
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := collector.CollectSpendLogs(ctx, from, to); err != nil {
			t.Fatalf("failed to collect spend logs: %v", err)
		}
		env.db.Where("tenant_id = ? AND call_type <> ''", tenantID).Find(&logs)
		seen := map[string]bool{}
		for _, l := range logs {
			seen[l.APIKey] = true
		}
		if seen[gateway.HashKey(oldKey)] && seen[gateway.HashKey(newKey)] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("spend of both keys was not attributed to the tenant after 60s, got %d rows", len(logs))
		}
		time.Sleep(2 * time.Second)
	}

	if _, err := mpmetering.NewUsagePipeline(env.db).AggregateDailyUsage(ctx, from, to); err != nil {
		t.Fatalf("failed to aggregate usage: %v", err)
	}
	var rows []mpentity.TenantUsageDaily
	env.db.Where("tenant_id = ?", tenantID).Find(&rows)
	if len(rows) != 1 || rows[0].CallCount < 2 {
		t.Fatalf("both keys must roll up into one usage row, got %+v", rows)
	}
}
