package reconciler

import (
	"context"
	"log/slog"
	"time"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ConvergeTenant finishes an interrupted rotation: it revokes every gateway key of a ready tenant except the
// one the record names and clears the dirty marker. It does nothing when the tenant is not ready or not dirty,
// so it is safe to call at any time.
func (r *KeyRotator) ConvergeTenant(ctx context.Context, db *gorm.DB, tenantID string) error {
	silentDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	instance, err := findPlatformInstance(ctx, silentDB, tenantID)
	if err != nil {
		return err
	}

	return WithInstanceWhitelistLock(ctx, silentDB, instance.ID, func() error {
		// Read under the lock: a rotation that held it may just have committed a new key.
		rec, err := r.reconcileDAO.GetByTenantID(ctx, silentDB, tenantID)
		if err != nil {
			return err
		}
		if rec.Status != entity.StatusReady || rec.KeyDirtySince == nil {
			return nil
		}
		revokeCtx, cancel := context.WithTimeout(ctx, rotationGatewayTimeout)
		defer cancel()
		revoked, err := r.converge(revokeCtx, silentDB, tenantID, rec.KeyHash)
		if err != nil {
			return err
		}
		slog.Info("tenant gateway keys converged", "tenant_id", tenantID, "key_hash", rec.KeyHash, "revoked_keys", revoked)
		return nil
	})
}

// ConvergeDirtyTenants converges the ready tenants that have been dirty for longer than minAge, which gives a
// rotation in progress time to finish on its own. It walks all of them in pages of batch tenants, so a tenant
// that keeps failing never hides the ones behind it. It returns the number of converged tenants; a failing
// tenant does not stop the others and is retried by the next sweep.
func (r *KeyRotator) ConvergeDirtyTenants(ctx context.Context, db *gorm.DB, minAge time.Duration, batch int) (int, error) {
	cutoff := time.Now().Add(-minAge)
	converged, after := 0, ""
	for {
		tenantIDs, err := r.reconcileDAO.FindDirtyKeyTenants(ctx, db, cutoff, after, batch)
		if err != nil {
			return converged, err
		}
		for _, tenantID := range tenantIDs {
			if err := ctx.Err(); err != nil {
				return converged, err
			}
			if err := r.ConvergeTenant(ctx, db, tenantID); err != nil {
				slog.Warn("tenant gateway key convergence failed", "tenant_id", tenantID, "error", err)
				continue
			}
			converged++
		}
		if len(tenantIDs) == 0 || len(tenantIDs) < batch {
			return converged, nil
		}
		after = tenantIDs[len(tenantIDs)-1]
	}
}
