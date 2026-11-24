package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TenantDecommissioner coordinates the lifecycle decommissioning of tenants,
// revoking LiteLLM virtual keys to reclaim quota and deactivating platform model records.
type TenantDecommissioner struct {
	gatewayClient GatewayKeyClient
	reconcileDAO  *dao.ReconcileDAO
}

// NewTenantDecommissioner creates a new TenantDecommissioner.
func NewTenantDecommissioner(client GatewayKeyClient) *TenantDecommissioner {
	return &TenantDecommissioner{
		gatewayClient: client,
		reconcileDAO:  dao.NewReconcileDAO(),
	}
}

// RegisterWithEventBus binds the decommissioner to listen for TenantDeleted events.
func (d *TenantDecommissioner) RegisterWithEventBus(bus *event.EventBus, db *gorm.DB) func() {
	if bus == nil || db == nil {
		return func() {}
	}
	return bus.Subscribe(event.TenantDeleted, func(ctx context.Context, evt event.TenantLifecycleEvent) error {
		return d.DecommissionTenant(ctx, db, evt.TenantID)
	})
}

// DecommissionTenant physically revokes every gateway virtual key of the tenant,
// transitions the reconcile state to 'deleted', and deactivates platform instances.
func (d *TenantDecommissioner) DecommissionTenant(ctx context.Context, db *gorm.DB, tenantID string) error {
	if db == nil {
		return dao.ErrNilTransaction
	}
	if tenantID == "" {
		return dao.ErrEmptyTenantID
	}
	if d.gatewayClient == nil {
		return errors.New("gateway client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	// 1. Fetch reconcile state
	rec, err := d.reconcileDAO.GetByTenantID(ctx, silentDB, tenantID)
	if err != nil {
		if errors.Is(err, entity.ErrRecordNotFound) {
			// No platform reconcile record exists for this tenant
			return nil
		}
		return err
	}

	// Idempotent: already decommissioned
	if rec.Status == entity.StatusDeleted {
		return nil
	}

	// 2. Revoke every virtual key of the tenant's gateway team. The team is the tenant's identity at the
	// gateway, so this works without the raw token and also removes keys left behind by failed rotations.
	revokeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := RevokeTenantKeys(revokeCtx, d.gatewayClient, tenantID); err != nil {
		return fmt.Errorf("failed to revoke virtual keys from gateway: %w", err)
	}

	// 3. Update database state in transaction: mark deleted and scrub key
	return silentDB.Transaction(func(tx *gorm.DB) error {
		// A. Transition Reconcile record to StatusDeleted with conflict resolution
		_, err := d.reconcileDAO.MarkDeleted(ctx, tx, tenantID, rec.Version)
		if err != nil {
			if errors.Is(err, entity.ErrOptimisticLockConflict) {
				latest, errFetch := d.reconcileDAO.GetByTenantID(ctx, tx, tenantID)
				if errFetch == nil && latest.Status == entity.StatusDeleted {
					return nil
				}
			}
			return fmt.Errorf("failed to mark reconcile record deleted: %w", err)
		}

		// B. Deactivate platform model instances and scrub their raw API keys
		var instanceIDs []string
		err = tx.Model(&rentity.TenantModelInstance{}).
			Where("instance_name = ?", PlatformInstanceName).
			Where("provider_id IN (?)", tx.Model(&rentity.TenantModelProvider{}).
				Select("id").
				Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible)).
			Pluck("id", &instanceIDs).Error
		if err != nil {
			return fmt.Errorf("failed to query model instances: %w", err)
		}

		if len(instanceIDs) > 0 {
			err = tx.Model(&rentity.TenantModelInstance{}).
				Where("id IN ?", instanceIDs).
				Updates(map[string]interface{}{
					"status":  "deleted",
					"api_key": "",
				}).Error
			if err != nil {
				return fmt.Errorf("failed to deactivate model instance: %w", err)
			}

			// C. Deactivate managed models
			err = tx.Model(&rentity.TenantModel{}).
				Where("instance_id IN ?", instanceIDs).
				Update("status", "deleted").Error
			if err != nil {
				return fmt.Errorf("failed to deactivate models: %w", err)
			}
		}

		return nil
	})
}

// ScanAndDecommissionOrphanedTenants audits platform_tenant_reconcile records and
// decommissions any tenants that were hard-deleted or soft-deleted in the upstream tenant table.
func (d *TenantDecommissioner) ScanAndDecommissionOrphanedTenants(ctx context.Context, db *gorm.DB) (int, error) {
	if db == nil {
		return 0, dao.ErrNilTransaction
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	const batchSize = 100
	var lastCreatedAt time.Time
	var lastTenantID string
	var errs []error
	decommissioned := 0

	for {
		select {
		case <-ctx.Done():
			return decommissioned, errors.Join(append(errs, ctx.Err())...)
		default:
		}

		query := silentDB.Where("status != ?", entity.StatusDeleted)
		if !lastCreatedAt.IsZero() {
			query = query.Where("(created_at > ?) OR (created_at = ? AND tenant_id > ?)", lastCreatedAt, lastCreatedAt, lastTenantID)
		}

		var records []entity.TenantReconcileRecord
		err := query.Order("created_at ASC, tenant_id ASC").Limit(batchSize).Find(&records).Error
		if err != nil {
			return decommissioned, errors.Join(append(errs, err)...)
		}
		if len(records) == 0 {
			break
		}

		for _, rec := range records {
			select {
			case <-ctx.Done():
				return decommissioned, errors.Join(append(errs, ctx.Err())...)
			default:
			}

			var count int64
			err := silentDB.Table("tenant").
				Where("id = ? AND status = ?", rec.TenantID, "1").
				Count(&count).Error
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to query upstream tenant status for %s: %w", rec.TenantID, err))
				continue
			}

			if count == 0 {
				// Tenant is inactive or absent: decommission!
				if err := d.DecommissionTenant(ctx, db, rec.TenantID); err != nil {
					errs = append(errs, fmt.Errorf("failed to decommission orphaned tenant %s: %w", rec.TenantID, err))
				} else {
					decommissioned++
				}
			}
		}

		lastRec := records[len(records)-1]
		lastCreatedAt = lastRec.CreatedAt
		lastTenantID = lastRec.TenantID

		if len(records) < batchSize {
			break
		}
	}

	return decommissioned, errors.Join(errs...)
}
