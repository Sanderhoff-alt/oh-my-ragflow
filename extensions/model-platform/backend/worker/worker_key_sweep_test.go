package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func TestStart_SweepsKeysLeftByAnInterruptedRotation(t *testing.T) {
	current, stale := gateway.HashKey("sk-current"), gateway.HashKey("sk-stale")
	var mu sync.Mutex
	var revoked []string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/key/list":
			_, _ = w.Write([]byte(`{"keys":[{"token":"` + current + `"},{"token":"` + stale + `"}],"total_pages":1}`))
		case "/key/delete":
			var req gateway.DeleteKeyRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			revoked = append(revoked, req.Keys...)
			mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer gw.Close()

	db := setupDB(t)
	tenantID := "tenant-interrupted-rotation"
	dirtySince := time.Now().Add(-time.Hour)
	db.Create(&entity.TenantReconcileRecord{
		TenantID: tenantID, Status: entity.StatusReady, MaxRetries: 50, Version: 1,
		KeyHash: current, KeyDirtySince: &dirtySince,
	})
	db.Create(&rentity.TenantModelProvider{ID: "p-sweep", TenantID: tenantID, ProviderName: "OpenAI-API-Compatible"})
	db.Create(&rentity.TenantModelInstance{ID: "i-sweep", InstanceName: "platform", ProviderID: "p-sweep", APIKey: "sk-current", Status: "active"})
	db.Exec("INSERT INTO tenant (id, status) VALUES (?, '1')", tenantID)
	configureGateway(t, gw.URL)
	SetDBProvider(func() *gorm.DB { return db })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers, err := Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer workers.Stop()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var rec entity.TenantReconcileRecord
		db.Where("tenant_id = ?", tenantID).First(&rec)
		if rec.KeyDirtySince == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the interrupted rotation was never converged")
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(revoked) != 1 || revoked[0] != stale {
		t.Fatalf("expected only the stale key to be revoked, got %s", strings.Join(revoked, ","))
	}
}
