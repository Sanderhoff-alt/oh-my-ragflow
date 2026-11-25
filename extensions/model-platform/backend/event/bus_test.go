package event

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestEventBus_BasicPublishSubscribe(t *testing.T) {
	bus := NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	var receivedCount int32
	var lastTenantID string
	var mu sync.Mutex
	done := make(chan struct{})

	unsub := bus.Subscribe(TenantCreated, func(ctx context.Context, evt TenantLifecycleEvent) error {
		atomic.AddInt32(&receivedCount, 1)
		mu.Lock()
		lastTenantID = evt.TenantID
		mu.Unlock()
		close(done)
		return nil
	})
	defer unsub()

	event := NewTenantCreatedEvent("tenant-123")
	ok := bus.Publish(event)
	if !ok {
		t.Fatal("expected publish to succeed")
	}

	select {
	case <-done:
		if atomic.LoadInt32(&receivedCount) != 1 {
			t.Errorf("expected 1 event, got %d", receivedCount)
		}
		mu.Lock()
		if lastTenantID != "tenant-123" {
			t.Errorf("expected tenant-123, got %s", lastTenantID)
		}
		mu.Unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event delivery")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus(100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)
	defer bus.Stop()

	var count int32
	unsub := bus.Subscribe(TenantCreated, func(ctx context.Context, evt TenantLifecycleEvent) error {
		atomic.AddInt32(&count, 1)
		return nil
	})

	bus.Publish(NewTenantCreatedEvent("t1"))
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected count 1, got %d", count)
	}

	// Unsubscribe
	unsub()

	bus.Publish(NewTenantCreatedEvent("t2"))
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&count) != 1 {
		t.Fatalf("expected count still 1 after unsubscribe, got %d", count)
	}
}

func TestEventBus_PublishTimeoutWhenBufferFull(t *testing.T) {
	// Tiny buffer of 1
	bus := NewEventBus(1)
	defer bus.Stop()

	// Fill the buffer
	bus.Publish(NewTenantCreatedEvent("t1"))

	// Non-blocking publish should return false
	if bus.Publish(NewTenantCreatedEvent("t2")) {
		t.Fatal("expected non-blocking publish to return false when buffer is full")
	}

	// PublishWithTimeout should return ErrPublishTimeout
	ctx := context.Background()
	err := bus.PublishWithTimeout(ctx, NewTenantCreatedEvent("t3"), 20*time.Millisecond)
	if err != ErrPublishTimeout {
		t.Fatalf("expected ErrPublishTimeout, got %v", err)
	}
}

func TestEventBus_HasSubscribersAndContext(t *testing.T) {
	var nilBus *EventBus
	if nilBus.HasSubscribers(TenantCreated) {
		t.Fatal("expected nil bus to report no subscribers")
	}
	select {
	case <-nilBus.Context().Done():
	default:
		t.Fatal("expected nil bus context to be canceled")
	}

	bus := NewEventBus(16)
	if bus.HasSubscribers(TenantCreated) {
		t.Fatal("expected fresh bus to have no subscribers")
	}

	unsub := bus.Subscribe(TenantCreated, func(ctx context.Context, evt TenantLifecycleEvent) error {
		return nil
	})
	if !bus.HasSubscribers(TenantCreated) {
		t.Fatal("expected bus to report subscriber for TenantCreated")
	}
	if bus.HasSubscribers(EventType("other.event")) {
		t.Fatal("expected bus to report no subscriber for unrelated event")
	}

	unsub()
	if bus.HasSubscribers(TenantCreated) {
		t.Fatal("expected bus to report no subscriber after unsubscribe")
	}

	unsubWildcard := bus.Subscribe("*", func(ctx context.Context, evt TenantLifecycleEvent) error {
		return nil
	})
	defer unsubWildcard()
	if !bus.HasSubscribers(TenantCreated) {
		t.Fatal("expected wildcard subscription to satisfy HasSubscribers")
	}

	busCtx := bus.Context()
	select {
	case <-busCtx.Done():
		t.Fatal("expected running bus context not to be canceled")
	default:
	}

	bus.Stop()
	if bus.HasSubscribers(TenantCreated) {
		t.Fatal("expected stopped bus to report no subscribers")
	}
	select {
	case <-busCtx.Done():
	default:
		t.Fatal("expected bus context to be canceled after Stop")
	}
}

func TestPreFlightCheck_ImmediateSuccess(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	// Setup tables
	db.Exec("CREATE TABLE tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")

	// Insert committed records
	db.Exec("INSERT INTO tenant (id, status) VALUES ('tenant-ok', '1');")
	db.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES ('ut-1', 'tenant-ok');")

	err = PreFlightCheck(context.Background(), db, "tenant-ok", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("expected pre-flight check to pass, got: %v", err)
	}
}

func TestPreFlightCheck_DelayedVisibility(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	db.Exec("CREATE TABLE tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")

	// Simulate delayed commit from another routine
	go func() {
		time.Sleep(30 * time.Millisecond)
		db.Exec("INSERT INTO tenant (id, status) VALUES ('tenant-delayed', '1');")
		db.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES ('ut-2', 'tenant-delayed');")
	}()

	err = PreFlightCheck(context.Background(), db, "tenant-delayed", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("expected pre-flight check to succeed after delayed visibility, got: %v", err)
	}
}

func TestPreFlightCheck_TimeoutWhenNotCommitted(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	db.Exec("CREATE TABLE tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")

	// Tenant exists, but user_tenant does NOT
	db.Exec("INSERT INTO tenant (id, status) VALUES ('tenant-incomplete', '1');")

	err = PreFlightCheck(context.Background(), db, "tenant-incomplete", 50*time.Millisecond)
	if err != ErrTenantNotCommitted {
		t.Fatalf("expected ErrTenantNotCommitted, got: %v", err)
	}
}

func TestPreFlightCheck_Validations(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	if err := PreFlightCheck(context.Background(), nil, "t1", 50*time.Millisecond); err != ErrNilDB {
		t.Errorf("expected ErrNilDB, got %v", err)
	}

	if err := PreFlightCheck(context.Background(), db, "", 50*time.Millisecond); err != ErrInvalidTenantID {
		t.Errorf("expected ErrInvalidTenantID, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PreFlightCheck(ctx, db, "t1", 50*time.Millisecond); err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestPreFlightCheck_FailsFastOnInactiveTenant(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	db.Exec("CREATE TABLE tenant (id VARCHAR(32) PRIMARY KEY, status VARCHAR(1));")
	db.Exec("CREATE TABLE user_tenant (id VARCHAR(32) PRIMARY KEY, tenant_id VARCHAR(32));")

	db.Exec("INSERT INTO tenant (id, status) VALUES ('tenant-inactive', '0');")
	db.Exec("INSERT INTO user_tenant (id, tenant_id) VALUES ('ut-inactive', 'tenant-inactive');")

	start := time.Now()
	err = PreFlightCheck(context.Background(), db, "tenant-inactive", 2*time.Second)
	if err != ErrTenantInactive {
		t.Fatalf("expected ErrTenantInactive, got: %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("expected fast failure on inactive tenant, took %v", time.Since(start))
	}
}
