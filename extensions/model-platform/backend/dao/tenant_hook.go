package dao

import (
	"context"
	"errors"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"

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
	go func() {
		if err := event.PreFlightCheck(context.Background(), rootDB, tenantID, 3*time.Second); err == nil {
			event.GetDefaultBus().Publish(event.NewTenantCreatedEvent(tenantID))
		}
	}()

	return nil
}
