package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := db.AutoMigrate(&entity.TenantReconcileRecord{}, &entity.GatewaySpendLog{}, &entity.TenantUsageDaily{}); err != nil {
		t.Fatalf("failed to migrate model platform tables: %v", err)
	}
	db.Exec("CREATE TABLE tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")
	return db
}

func configureGateway(t *testing.T, url string) {
	t.Helper()
	t.Setenv("LITELLM_BASE_URL", url)
	t.Setenv("LITELLM_MASTER_KEY", "sk-test-master")
}

func TestStart_GatewayNotConfigured(t *testing.T) {
	t.Setenv("LITELLM_BASE_URL", "")
	t.Setenv("LITELLM_MASTER_KEY", "")

	if _, err := Start(context.Background()); !errors.Is(err, ErrGatewayNotConfigured) {
		t.Fatalf("expected ErrGatewayNotConfigured, got %v", err)
	}
}

func TestStart_NoDatabase(t *testing.T) {
	configureGateway(t, "http://127.0.0.1:1")
	SetDBProvider(nil)

	if _, err := Start(context.Background()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("expected ErrNoDatabase, got %v", err)
	}

	SetDBProvider(func() *gorm.DB { return nil })
	if _, err := Start(context.Background()); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("expected ErrNoDatabase for nil handle, got %v", err)
	}
}

func TestStart_TenantCreationReachesGateway(t *testing.T) {
	var generateCalls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/key/generate" {
			generateCalls.Add(1)
			_, _ = w.Write([]byte(`{"key":"sk-tenant-key"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer gw.Close()

	db := setupDB(t)
	configureGateway(t, gw.URL)
	SetDBProvider(func() *gorm.DB { return db })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers, err := Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer workers.Stop()

	// Mirror the registration flow: the hook runs in a nested transaction inside the caller's transaction.
	const tenantID = "tenant-wired-1"
	err = db.Transaction(func(outer *gorm.DB) error {
		if err := outer.Transaction(func(inner *gorm.DB) error {
			if err := dao.OnTenantCreateHook(ctx, inner, tenantID); err != nil {
				return err
			}
			return inner.Exec("INSERT INTO tenant (id, status) VALUES (?, '1')", tenantID).Error
		}); err != nil {
			return err
		}
		return outer.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES (?, ?)", "ut-"+tenantID, tenantID).Error
	})
	if err != nil {
		t.Fatalf("tenant creation transaction failed: %v", err)
	}

	// The 30s poller cannot explain a hit this early: only the event path can.
	deadline := time.Now().Add(3 * time.Second)
	for generateCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("gateway never received /key/generate after tenant creation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStart_PollsSpendLogs(t *testing.T) {
	var spendLogCalls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/spend/logs/v2" {
			spendLogCalls.Add(1)
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer gw.Close()

	db := setupDB(t)
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
	for spendLogCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("metering daemon never polled /spend/logs/v2")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
