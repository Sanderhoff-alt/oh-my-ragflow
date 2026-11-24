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
	rentity "ragflow/internal/entity"

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

	if err := db.AutoMigrate(
		&entity.TenantReconcileRecord{},
		&entity.GatewaySpendLog{},
		&entity.TenantUsageDaily{},
		&entity.PlatformManagedModel{},
		&entity.ModelPublishTask{},
		&entity.ModelPublishItem{},
		&rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{},
		&rentity.TenantModel{},
	); err != nil {
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

func TestStart_DecommissionsOrphanedTenant(t *testing.T) {
	var deleteCalls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/key/delete" {
			deleteCalls.Add(1)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer gw.Close()

	db := setupDB(t)
	now := time.Now()
	// The active tenant is older, so the scan evaluates it before the orphan.
	records := []entity.TenantReconcileRecord{
		{TenantID: "tenant-active", Status: entity.StatusReady, MaxRetries: 50, Version: 1, CreatedAt: now.Add(-2 * time.Hour)},
		{TenantID: "tenant-orphan", Status: entity.StatusReady, MaxRetries: 50, Version: 1, CreatedAt: now.Add(-time.Hour)},
	}
	if err := db.Create(&records).Error; err != nil {
		t.Fatalf("failed to seed reconcile records: %v", err)
	}
	if err := db.Exec("INSERT INTO tenant (id, status) VALUES ('tenant-active', '1')").Error; err != nil {
		t.Fatalf("failed to seed tenant: %v", err)
	}
	configureGateway(t, gw.URL)
	SetDBProvider(func() *gorm.DB { return db })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workers, err := Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer workers.Stop()

	statusOf := func(tenantID string) entity.ReconcileStatus {
		var rec entity.TenantReconcileRecord
		if err := db.Where("tenant_id = ?", tenantID).First(&rec).Error; err != nil {
			t.Fatalf("failed to load record %s: %v", tenantID, err)
		}
		return rec.Status
	}
	deadline := time.Now().Add(3 * time.Second)
	for statusOf("tenant-orphan") != entity.StatusDeleted {
		if time.Now().After(deadline) {
			t.Fatal("orphaned tenant was never decommissioned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := deleteCalls.Load(); got != 1 {
		t.Fatalf("expected exactly one /key/delete call, got %d", got)
	}
	if got := statusOf("tenant-active"); got != entity.StatusReady {
		t.Fatalf("active tenant must stay ready, got %s", got)
	}
}
