package dao

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
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

	// Auto-migrate tables
	if err := db.AutoMigrate(&entity.TenantReconcileRecord{}); err != nil {
		t.Fatalf("failed to migrate reconcile record: %v", err)
	}
	db.Exec("CREATE TABLE IF NOT EXISTS tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE IF NOT EXISTS user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")

	return db
}

func TestOnTenantCreateHook_ExecutionLatency(t *testing.T) {
	db := setupTestDB(t)

	// Measure 50 consecutive hook executions inside transactions
	const iterations = 50
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		tenantID := fmt.Sprintf("tenant-perf-%d", i)
		start := time.Now()
		err := db.Transaction(func(tx *gorm.DB) error {
			return OnTenantCreateHook(context.Background(), tx, tenantID)
		})
		elapsed := time.Since(start)
		totalDuration += elapsed

		if err != nil {
			t.Fatalf("hook execution failed: %v", err)
		}
	}

	avgDuration := totalDuration / iterations
	t.Logf("Average hook execution latency: %v", avgDuration)
	if avgDuration > 5*time.Millisecond {
		t.Errorf("expected average latency < 5ms, got %v", avgDuration)
	}
}

func TestOnTenantCreateHook_CommitDispatchesEvent(t *testing.T) {
	db := setupTestDB(t)

	bus := event.NewEventBus(100)
	event.SetDefaultBus(bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	received := make(chan string, 1)
	unsub := bus.Subscribe(event.TenantCreated, func(c context.Context, evt event.TenantLifecycleEvent) error {
		select {
		case received <- evt.TenantID:
		default:
		}
		return nil
	})
	defer unsub()

	tenantID := "tenant-committed-1"

	// Execute transaction simulating UserService.CreateUserTenant
	err := db.Transaction(func(tx *gorm.DB) error {
		// 1. Hook called
		if err := OnTenantCreateHook(context.Background(), tx, tenantID); err != nil {
			return err
		}
		// 2. Tenant and user_tenant committed
		if err := tx.Exec("INSERT INTO tenant (id, status) VALUES (?, '1')", tenantID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES (?, ?)", "ut-"+tenantID, tenantID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	// Verify DB record
	var record entity.TenantReconcileRecord
	if err := db.Where("tenant_id = ?", tenantID).First(&record).Error; err != nil {
		t.Fatalf("reconcile record not found in db: %v", err)
	}
	if record.Status != entity.StatusPending {
		t.Errorf("expected status pending, got %s", record.Status)
	}

	// Verify post-commit event dispatch
	select {
	case id := <-received:
		if id != tenantID {
			t.Errorf("expected event for tenant %s, got %s", tenantID, id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for post-commit event dispatch")
	}

	// Verify pre-flight check succeeds on committed data
	if err := event.PreFlightCheck(context.Background(), db, tenantID, 100*time.Millisecond); err != nil {
		t.Fatalf("pre-flight check failed on committed tenant: %v", err)
	}
}

func TestOnTenantCreateHook_NestedTransaction_DispatchesEvent(t *testing.T) {
	db := setupTestDB(t)

	bus := event.NewEventBus(100)
	event.SetDefaultBus(bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	received := make(chan string, 1)
	unsub := bus.Subscribe(event.TenantCreated, func(c context.Context, evt event.TenantLifecycleEvent) error {
		select {
		case received <- evt.TenantID:
		default:
		}
		return nil
	})
	defer unsub()

	tenantID := "tenant-nested-1"

	// Simulate exact call hierarchy of UserService.CreateUserTenant -> TenantDAO.Create
	err := db.Transaction(func(outerTx *gorm.DB) error {
		// Simulating TenantDAO.Create(ctx, outerTx, tenant) with nested WithContext().Transaction()
		if err := outerTx.WithContext(context.Background()).Transaction(func(innerTx *gorm.DB) error {
			if err := OnTenantCreateHook(context.Background(), innerTx, tenantID); err != nil {
				return err
			}
			return innerTx.Exec("INSERT INTO tenant (id, status) VALUES (?, '1')", tenantID).Error
		}); err != nil {
			return err
		}
		return outerTx.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES (?, ?)", "ut-"+tenantID, tenantID).Error
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	// Verify post-commit event dispatch
	select {
	case id := <-received:
		if id != tenantID {
			t.Errorf("expected event for tenant %s, got %s", tenantID, id)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for post-commit event dispatch in nested transaction")
	}
}

func TestOnTenantCreateHook_RollbackSuppressesEvent(t *testing.T) {
	db := setupTestDB(t)

	bus := event.NewEventBus(100)
	event.SetDefaultBus(bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	var eventCount int32
	unsub := bus.Subscribe(event.TenantCreated, func(c context.Context, evt event.TenantLifecycleEvent) error {
		atomic.AddInt32(&eventCount, 1)
		return nil
	})
	defer unsub()

	tenantID := "tenant-rolled-back-1"

	// Transaction that fails and rolls back
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := OnTenantCreateHook(context.Background(), tx, tenantID); err != nil {
			return err
		}
		// Simulate failure in later step (e.g. userTenant insert failure)
		return errors.New("simulated error forcing transaction rollback")
	})

	if err == nil {
		t.Fatal("expected transaction error, got nil")
	}

	// Verify DB record does NOT exist
	var count int64
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 records after rollback, got %d", count)
	}

	// Wait 100ms to guarantee no event is dispatched
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&eventCount) != 0 {
		t.Errorf("expected 0 events dispatched on rollback, got %d", eventCount)
	}
}

func TestOnTenantCreateHook_Idempotency(t *testing.T) {
	db := setupTestDB(t)
	tenantID := "tenant-idempotent-1"

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := OnTenantCreateHook(context.Background(), tx, tenantID); err != nil {
			return err
		}
		// Call again within same transaction
		if err := OnTenantCreateHook(context.Background(), tx, tenantID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("idempotent hook execution failed: %v", err)
	}

	var count int64
	db.Model(&entity.TenantReconcileRecord{}).Where("tenant_id = ?", tenantID).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 record, got %d", count)
	}
}

func TestOnTenantCreateHook_Validation(t *testing.T) {
	db := setupTestDB(t)

	if err := OnTenantCreateHook(context.Background(), nil, "t1"); err != ErrNilTransaction {
		t.Errorf("expected ErrNilTransaction, got %v", err)
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		return OnTenantCreateHook(context.Background(), tx, "")
	})
	if err != ErrEmptyTenantID {
		t.Errorf("expected ErrEmptyTenantID, got %v", err)
	}
}

func TestOnTenantCreateHook_ConcurrentRegistrations(t *testing.T) {
	db := setupTestDB(t)
	const concurrentUsers = 20

	bus := event.NewEventBus(200)
	event.SetDefaultBus(bus)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	var receivedCount int32
	unsub := bus.Subscribe(event.TenantCreated, func(c context.Context, evt event.TenantLifecycleEvent) error {
		atomic.AddInt32(&receivedCount, 1)
		return nil
	})
	defer unsub()

	var wg sync.WaitGroup
	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tID := fmt.Sprintf("tenant-concurrent-%d", idx)
			_ = db.Transaction(func(tx *gorm.DB) error {
				if err := OnTenantCreateHook(context.Background(), tx, tID); err != nil {
					return err
				}
				_ = tx.Exec("INSERT INTO tenant (id, status) VALUES (?, '1')", tID).Error
				_ = tx.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES (?, ?)", "ut-"+tID, tID).Error
				return nil
			})
		}(i)
	}

	wg.Wait()

	// Wait for all events to be dispatched
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&receivedCount) == concurrentUsers {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if atomic.LoadInt32(&receivedCount) != concurrentUsers {
		t.Errorf("expected %d events, got %d", concurrentUsers, receivedCount)
	}
}
