package dao

import (
	"context"
	"errors"
	"time"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// ReconcileDAO provides persistence and optimistic locking state transition operations
// for platform_tenant_reconcile records.
type ReconcileDAO struct{}

// NewReconcileDAO instantiates a new ReconcileDAO.
func NewReconcileDAO() *ReconcileDAO {
	return &ReconcileDAO{}
}

// GetByTenantID fetches the reconcile record for the given tenant ID.
// Returns entity.ErrRecordNotFound if no record exists.
func (d *ReconcileDAO) GetByTenantID(ctx context.Context, db *gorm.DB, tenantID string) (*entity.TenantReconcileRecord, error) {
	if db == nil {
		return nil, ErrNilTransaction
	}
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	var record entity.TenantReconcileRecord
	err := db.WithContext(ctx).Where("tenant_id = ?", tenantID).First(&record).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, entity.ErrRecordNotFound
		}
		return nil, err
	}
	return &record, nil
}

// ClaimForProvisioning transitions a record from 'pending' or 'degraded' to 'provisioning'
// using optimistic locking on the record's version column.
// Records already in 'provisioning' cannot be claimed.
func (d *ReconcileDAO) ClaimForProvisioning(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if current.Version != currentVersion {
		return nil, entity.ErrOptimisticLockConflict
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusProvisioning); err != nil {
		return nil, err
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ? AND status != ?", tenantID, currentVersion, entity.StatusProvisioning).
		Updates(map[string]interface{}{
			"status":     entity.StatusProvisioning,
			"version":    currentVersion + 1,
			"updated_at": time.Now(),
		})

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// ClaimDueForProvisioning transitions a record from 'pending' or 'degraded' to 'provisioning'
// using optimistic locking, but atomically requires next_retry_at to be NULL or <= now.
// If another worker pushed next_retry_at into the future, the claim fails with ErrOptimisticLockConflict.
func (d *ReconcileDAO) ClaimDueForProvisioning(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, now time.Time) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if current.Version != currentVersion {
		return nil, entity.ErrOptimisticLockConflict
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusProvisioning); err != nil {
		return nil, err
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ? AND status != ? AND (next_retry_at IS NULL OR next_retry_at <= ?)",
			tenantID, currentVersion, entity.StatusProvisioning, now).
		Updates(map[string]interface{}{
			"status":     entity.StatusProvisioning,
			"version":    currentVersion + 1,
			"updated_at": time.Now(),
		})

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// MarkReady transitions a record to 'ready', storing the generated gateway virtual key alias
// and key hash, and resetting the retry counter.
func (d *ReconcileDAO) MarkReady(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, gatewayKeyAlias, keyHash string) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusReady); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{
		"status":            entity.StatusReady,
		"gateway_key_alias": gatewayKeyAlias,
		"key_hash":          keyHash,
		"retry_count":       0,
		"next_retry_at":     nil,
		"last_error":        "",
		"version":           currentVersion + 1,
		"updated_at":        time.Now(),
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ?", tenantID, currentVersion).
		Updates(updates)

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// MarkDegraded transitions a record to 'degraded', incrementing retry count and scheduling
// the next retry attempt.
func (d *ReconcileDAO) MarkDegraded(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, lastError string, nextRetryAt time.Time) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusDegraded); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{
		"status":        entity.StatusDegraded,
		"retry_count":   gorm.Expr("retry_count + ?", 1),
		"next_retry_at": nextRetryAt,
		"last_error":    lastError,
		"version":       currentVersion + 1,
		"updated_at":    time.Now(),
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ?", tenantID, currentVersion).
		Updates(updates)

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// MarkFailed transitions a record to 'failed', marking it as requiring manual intervention.
func (d *ReconcileDAO) MarkFailed(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, fatalError string) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusFailed); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{
		"status":        entity.StatusFailed,
		"last_error":    fatalError,
		"next_retry_at": nil,
		"version":       currentVersion + 1,
		"updated_at":    time.Now(),
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ?", tenantID, currentVersion).
		Updates(updates)

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// MarkDeleted transitions a record to 'deleted' when a tenant is decommissioned.
func (d *ReconcileDAO) MarkDeleted(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64) (*entity.TenantReconcileRecord, error) {
	current, err := d.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	if err := entity.ValidateTransition(current.Status, entity.StatusDeleted); err != nil {
		return nil, err
	}

	updates := map[string]interface{}{
		"status":     entity.StatusDeleted,
		"version":    currentVersion + 1,
		"updated_at": time.Now(),
	}

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ?", tenantID, currentVersion).
		Updates(updates)

	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}

	return d.GetByTenantID(ctx, db, tenantID)
}

// FindDueRetries queries records in 'pending' or 'degraded' status that are due for processing,
// as well as stale 'provisioning' records whose lease has expired (> 2 minutes).
func (d *ReconcileDAO) FindDueRetries(ctx context.Context, db *gorm.DB, now time.Time, limit int) ([]*entity.TenantReconcileRecord, error) {
	if db == nil {
		return nil, ErrNilTransaction
	}
	if limit <= 0 {
		limit = 50
	}

	staleCutoff := now.Add(-2 * time.Minute)

	var records []*entity.TenantReconcileRecord
	err := db.WithContext(ctx).
		Where("(status IN (?, ?) AND (next_retry_at IS NULL OR next_retry_at <= ?)) OR (status = ? AND updated_at <= ?)",
			entity.StatusPending, entity.StatusDegraded, now, entity.StatusProvisioning, staleCutoff).
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error

	if err != nil {
		return nil, err
	}
	return records, nil
}
