package dao

import (
	"context"
	"errors"
	"strings"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
// It then dispatches the TenantCreated event once the outer transaction has committed.
func OnTenantCreateHook(ctx context.Context, tx *gorm.DB, tenantID string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if tenantID == "" {
		return ErrEmptyTenantID
	}

	record := entity.TenantReconcileRecord{
		TenantID:   tenantID,
		Status:     entity.StatusPending,
		RetryCount: 0,
		MaxRetries: 50,
		Version:    1,
	}
	if err := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error; err != nil {
		return err
	}

	// Skip background visibility polling when nothing listens for TenantCreated
	// (for example, unit tests that do not start model-platform workers).
	bus := event.GetDefaultBus()
	if !bus.HasSubscribers(event.TenantCreated) {
		return nil
	}

	// Nested transactions give no commit callback, so poll a root connection until the tenant is visible.
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

	// Unshared SQLite in-memory pools open a fresh empty database per connection.
	// Clamp the pool to a single connection so background PreFlightCheck queries
	// serialize onto the existing connection instead of allocating an empty one.
	clampUnsharedSQLiteMemoryPool(rootDB)

	busCtx := bus.Context()
	go func() {
		if err := event.PreFlightCheck(busCtx, rootDB, tenantID, 3*time.Second); err == nil {
			bus.Publish(event.NewTenantCreatedEvent(tenantID))
		}
	}()

	return nil
}

func isUnsharedSQLiteMemoryDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if lower == "" || lower == ":memory:" {
		return true
	}
	if strings.Contains(lower, ":memory:") || strings.Contains(lower, "mode=memory") {
		return !strings.Contains(lower, "cache=shared")
	}
	return false
}

func clampUnsharedSQLiteMemoryPool(db *gorm.DB) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return
	}
	var dsn string
	switch d := db.Dialector.(type) {
	case *sqlite.Dialector:
		dsn = d.DSN
	case sqlite.Dialector:
		dsn = d.DSN
	default:
		return
	}
	if !isUnsharedSQLiteMemoryDSN(dsn) {
		return
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB == nil {
		return
	}
	if sqlDB.Stats().MaxOpenConnections != 1 {
		sqlDB.SetMaxOpenConns(1)
	}
}
