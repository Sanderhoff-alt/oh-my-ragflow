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

// ListByStatus returns a page of the records in the given status, ordered by tenant id, that follow
// afterTenantID (empty for the first page), together with the total number of such records. Paging by cursor
// instead of by offset means a record that leaves the status between two pages cannot make the walk skip
// another one. It is not a snapshot: a record that enters the status behind the cursor is not returned.
func (d *ReconcileDAO) ListByStatus(ctx context.Context, db *gorm.DB, status entity.ReconcileStatus, afterTenantID string, limit int) ([]*entity.TenantReconcileRecord, int64, error) {
	if db == nil {
		return nil, 0, ErrNilTransaction
	}
	if limit <= 0 {
		limit = 100
	}

	query := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).Where("status = ?", status)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []*entity.TenantReconcileRecord
	if err := query.Where("tenant_id > ?", afterTenantID).Order("tenant_id ASC").Limit(limit).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// MarkKeyDirty records that a key rotation is starting, so the gateway may hold keys of the tenant besides
// the one named by key_hash. It is a compare-and-swap on a 'ready' record: a concurrent decommission or
// rotation makes it fail with ErrOptimisticLockConflict. The status stays 'ready'.
func (d *ReconcileDAO) MarkKeyDirty(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, now time.Time) (*entity.TenantReconcileRecord, error) {
	return d.updateReady(ctx, db, tenantID, currentVersion, map[string]interface{}{
		"key_dirty_since": now,
	})
}

// CommitKeyRotation points a 'ready' record at the newly issued key. It is meant to run in the same
// transaction that swaps the key the tenant's model instance uses.
func (d *ReconcileDAO) CommitKeyRotation(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, gatewayKeyAlias, keyHash, rotatedBy string, now time.Time) (*entity.TenantReconcileRecord, error) {
	return d.updateReady(ctx, db, tenantID, currentVersion, map[string]interface{}{
		"gateway_key_alias": gatewayKeyAlias,
		"key_hash":          keyHash,
		"key_rotated_at":    now,
		"key_rotated_by":    rotatedBy,
	})
}

// ClearKeyDirty marks the gateway keys of the tenant as converged on keyHash. It does nothing when the
// record already names another key, so a stale caller cannot clear the marker of a newer rotation.
func (d *ReconcileDAO) ClearKeyDirty(ctx context.Context, db *gorm.DB, tenantID, keyHash string) error {
	if db == nil {
		return ErrNilTransaction
	}
	return db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND key_hash = ?", tenantID, keyHash).
		Update("key_dirty_since", nil).Error
}

// FindDirtyKeyTenants returns a page of the 'ready' tenants whose gateway keys have not converged since before
// olderThan. Pages are ordered by tenant id and continue after afterTenantID, so a caller can walk all of
// them and a tenant that keeps failing cannot hold back the ones behind it.
func (d *ReconcileDAO) FindDirtyKeyTenants(ctx context.Context, db *gorm.DB, olderThan time.Time, afterTenantID string, limit int) ([]string, error) {
	if db == nil {
		return nil, ErrNilTransaction
	}
	if limit <= 0 {
		limit = 50
	}

	var tenantIDs []string
	err := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("status = ? AND key_dirty_since IS NOT NULL AND key_dirty_since <= ? AND tenant_id > ?", entity.StatusReady, olderThan, afterTenantID).
		Order("tenant_id ASC").
		Limit(limit).
		Pluck("tenant_id", &tenantIDs).Error
	if err != nil {
		return nil, err
	}
	return tenantIDs, nil
}

// updateReady applies updates to a 'ready' record under an optimistic version check and bumps the version.
func (d *ReconcileDAO) updateReady(ctx context.Context, db *gorm.DB, tenantID string, currentVersion int64, updates map[string]interface{}) (*entity.TenantReconcileRecord, error) {
	if db == nil {
		return nil, ErrNilTransaction
	}
	if tenantID == "" {
		return nil, ErrEmptyTenantID
	}

	updates["version"] = currentVersion + 1
	updates["updated_at"] = time.Now()

	res := db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
		Where("tenant_id = ? AND version = ? AND status = ?", tenantID, currentVersion, entity.StatusReady).
		Updates(updates)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, entity.ErrOptimisticLockConflict
	}
	return d.GetByTenantID(ctx, db, tenantID)
}
