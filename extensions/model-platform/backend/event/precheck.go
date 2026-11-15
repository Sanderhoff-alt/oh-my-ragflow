package event

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// ErrTenantNotCommitted indicates that tenant or user_tenant records were not visible before timeout.
	ErrTenantNotCommitted = errors.New("pre-flight check failed: tenant or user_tenant records not visible within timeout")
	// ErrTenantInactive indicates that the tenant exists but its status is inactive ("0").
	ErrTenantInactive = errors.New("pre-flight check failed: tenant is inactive")
	// ErrInvalidTenantID indicates that an empty tenantID was provided.
	ErrInvalidTenantID = errors.New("pre-flight check failed: tenantID cannot be empty")
	// ErrNilDB indicates that a nil database handle was passed.
	ErrNilDB = errors.New("pre-flight check failed: database instance cannot be nil")
)

// PreFlightCheck validates that the tenant and user_tenant records are committed and visible in the database.
// If the outer transaction is still completing, it retries with exponential backoff up to maxWait.
// If the transaction rolled back, the records will never become visible and ErrTenantNotCommitted is returned.
func PreFlightCheck(ctx context.Context, db *gorm.DB, tenantID string, maxWait time.Duration) error {
	if db == nil {
		return ErrNilDB
	}
	if tenantID == "" {
		return ErrInvalidTenantID
	}
	if maxWait <= 0 {
		maxWait = 500 * time.Millisecond
	}

	checkCtx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()

	interval := 5 * time.Millisecond
	maxInterval := 50 * time.Millisecond

	// Silence query error logs during polling
	silentDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if checkCtx.Err() != nil {
			return ErrTenantNotCommitted
		}

		var status string
		err := silentDB.WithContext(checkCtx).Table("tenant").
			Select("status").
			Where("id = ?", tenantID).
			Take(&status).Error

		if err == nil {
			if status != "1" {
				return ErrTenantInactive
			}
			var userTenantCount int64
			err = silentDB.WithContext(checkCtx).Table("user_tenant").
				Where("tenant_id = ?", tenantID).
				Count(&userTenantCount).Error

			if err == nil && userTenantCount > 0 {
				// Both active tenant and user_tenant are committed and visible!
				return nil
			}
			if err != nil && !silentDB.Migrator().HasTable("user_tenant") {
				return nil
			}
		}

		// Backoff sleep respecting context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-checkCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrTenantNotCommitted
		case <-time.After(interval):
		}

		interval *= 2
		if interval > maxInterval {
			interval = maxInterval
		}
	}
}
