package dao

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var (
	// ErrNilTransaction indicates a nil *gorm.DB was passed to the hook.
	ErrNilTransaction = errors.New("tenant hook requires a valid *gorm.DB instance")
	// ErrEmptyTenantID indicates tenantID was empty.
	ErrEmptyTenantID = errors.New("tenant hook requires a non-empty tenantID")
)

// OnTenantCreateHook is executed inside the tenant creation transaction (initializeTenantDefaultRoleEE).
// It strictly performs local SQL only (inserts a pending record into platform_tenant_reconcile),
// avoiding any external network calls to guarantee transaction execution within a few milliseconds.
// It also registers a Post-Commit Hook that dispatches the TenantCreated event strictly after the
// transaction has successfully committed to the database.
func OnTenantCreateHook(ctx context.Context, tx *gorm.DB, tenantID string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	// 1. Strictly local SQL execution: idempotent check and insert
	var existing entity.TenantReconcileRecord
	silentTx := tx.Session(&gorm.Session{Logger: tx.Logger.LogMode(logger.Silent)})
	err := silentTx.WithContext(ctx).Where("tenant_id = ?", tenantID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		record := entity.TenantReconcileRecord{
			TenantID:   tenantID,
			Status:     entity.StatusPending,
			RetryCount: 0,
			MaxRetries: 50,
			Version:    1,
		}
		if err := silentTx.WithContext(ctx).Create(&record).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// 2. Dispatch event to EventBus strictly after transaction commits
	var committed atomic.Bool
	var dispatchOnce sync.Once
	dispatch := func() {
		dispatchOnce.Do(func() {
			committed.Store(true)
			event.GetDefaultBus().Publish(event.NewTenantCreatedEvent(tenantID))
		})
	}

	// 2a. Direct post-commit wrapper (handles flat transactions and autocommit)
	event.RegisterPostCommitHook(tx, dispatch)

	// 2b. Background visibility watcher (handles nested transactions and statement clones)
	// If tx is a cloned handle in a nested transaction (e.g. UserService.CreateUserTenant),
	// wrapper.Commit may not be invoked directly by the outer commit.
	// We asynchronously poll until the records commit and become visible across connections.
	// If the transaction rolls back, PreFlightCheck fails closed without dispatching.
	pool := tx.ConnPool
	if pool == nil && tx.Statement != nil {
		pool = tx.Statement.ConnPool
	}
	rootDB := &gorm.DB{Config: tx.Config}
	rootDB.Statement = &gorm.Statement{
		DB:       rootDB,
		ConnPool: pool,
		Context:  context.Background(),
		Clauses:  map[string]clause.Clause{},
	}
	go func() {
		// Allow synchronous post-commit hook on flat transactions to dispatch first
		time.Sleep(10 * time.Millisecond)
		if committed.Load() {
			return
		}
		if err := event.PreFlightCheck(context.Background(), rootDB, tenantID, 3*time.Second); err == nil {
			dispatch()
		}
	}()

	return nil
}
