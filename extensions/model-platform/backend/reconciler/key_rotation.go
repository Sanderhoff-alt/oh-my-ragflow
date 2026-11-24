package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	// ErrInvalidTenantID is returned for a tenant id that cannot safely be used as a gateway team id.
	ErrInvalidTenantID = errors.New("invalid tenant id")
	// ErrTenantNotReady is returned when a tenant has no provisioned platform key to rotate.
	ErrTenantNotReady = errors.New("tenant is not ready for key rotation")
	// ErrRotationConflict is returned when the tenant changed state while its key was being rotated.
	ErrRotationConflict = errors.New("tenant changed during key rotation")
	// ErrGatewayFailure is returned when the gateway could not issue the replacement key.
	ErrGatewayFailure = errors.New("gateway failed to issue the replacement key")
)

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	rotationGatewayTimeout = 10 * time.Second
	rotationCleanupTimeout = 5 * time.Second
)

// RotationResult describes a completed key rotation.
type RotationResult struct {
	TenantID  string
	KeyAlias  string
	KeyHash   string
	RotatedAt time.Time
	// RevokedKeys is the number of gateway keys revoked by the rotation.
	RevokedKeys int
	// Converged is false when the new key is in use but a superseded key may still be valid at the gateway.
	// The key sweeper finishes the revocation, and rotating again resumes it immediately.
	Converged bool
}

// KeyRotator replaces the gateway virtual key of a tenant without interrupting it. A rotation issues the new
// key, swaps it into the tenant's model instance in one transaction, and then converges the tenant's gateway
// team on that key, which revokes every other key. The raw key is never returned or logged.
type KeyRotator struct {
	gatewayClient GatewayKeyClient
	reconcileDAO  *dao.ReconcileDAO
	allowedModels []string
}

// NewKeyRotator creates a KeyRotator. A non-empty allowedModels restricts the models of the issued keys like
// it does for NewTenantReconciler, an empty one permits every active model of the tenant.
func NewKeyRotator(client GatewayKeyClient, allowedModels []string) *KeyRotator {
	return &KeyRotator{
		gatewayClient: client,
		reconcileDAO:  dao.NewReconcileDAO(),
		allowedModels: allowedModels,
	}
}

// ValidateTenantID reports whether the tenant id may be used as a gateway team id.
func ValidateTenantID(tenantID string) error {
	if !tenantIDPattern.MatchString(tenantID) {
		return fmt.Errorf("%w: %q", ErrInvalidTenantID, tenantID)
	}
	return nil
}

// Rotate replaces the key of a ready tenant and attributes the rotation to operator.
//
// The record is marked dirty before anything changes at the gateway, so a crash at any later point leaves a
// marker that the key sweeper turns into a convergence. The status of the record stays 'ready': the poller
// treats a stale 'provisioning' record as a failed attempt and would issue yet another key.
func (r *KeyRotator) Rotate(ctx context.Context, db *gorm.DB, tenantID, operator string) (*RotationResult, error) {
	if r.gatewayClient == nil {
		return nil, errors.New("gateway client is not configured")
	}
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if err := ValidateTenantID(tenantID); err != nil {
		return nil, err
	}

	silentDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	if _, err := r.reconcileDAO.GetByTenantID(ctx, silentDB, tenantID); err != nil {
		return nil, err
	}
	instance, err := findPlatformInstance(ctx, silentDB, tenantID)
	if err != nil {
		return nil, err
	}

	var result *RotationResult
	err = WithInstanceWhitelistLock(ctx, silentDB, instance.ID, func() error {
		var rotateErr error
		result, rotateErr = r.rotateLocked(ctx, silentDB, tenantID, operator, instance.ID)
		return rotateErr
	})
	return result, err
}

func (r *KeyRotator) rotateLocked(ctx context.Context, db *gorm.DB, tenantID, operator, instanceID string) (*RotationResult, error) {
	// Read under the lock: the key and the whitelist may have changed while this call waited for it.
	rec, err := r.reconcileDAO.GetByTenantID(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}
	if rec.Status != entity.StatusReady {
		return nil, fmt.Errorf("%w: status is %q", ErrTenantNotReady, rec.Status)
	}
	var instance rentity.TenantModelInstance
	if err := db.WithContext(ctx).Where("id = ?", instanceID).First(&instance).Error; err != nil {
		return nil, fmt.Errorf("failed to read the managed instance: %w", err)
	}

	now := time.Now()
	if rec, err = r.reconcileDAO.MarkKeyDirty(ctx, db, tenantID, rec.Version, now); err != nil {
		return nil, rotationConflict(err)
	}

	whitelist, err := gatewayWhitelist(ctx, db, r.allowedModels, &instance, "", 0, gatewayOverrideNone)
	if err != nil {
		return nil, err
	}
	req := newTenantKeyRequest(tenantID, whitelist)
	issueCtx, cancel := context.WithTimeout(ctx, rotationGatewayTimeout)
	defer cancel()
	resp, err := r.gatewayClient.GenerateKey(issueCtx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGatewayFailure, err)
	}
	if resp.Key == "" {
		return nil, fmt.Errorf("%w: gateway returned an empty key", ErrGatewayFailure)
	}
	newHash := gateway.HashKey(resp.Key)
	alias := resp.KeyName
	if alias == "" {
		alias = req.KeyName
	}

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&rentity.TenantModelInstance{}).Where("id = ?", instanceID).
			Updates(map[string]interface{}{"api_key": resp.Key, "status": "active"}).Error; err != nil {
			return err
		}
		_, err := r.reconcileDAO.CommitKeyRotation(ctx, tx, tenantID, rec.Version, alias, newHash, operator, now)
		return err
	})
	if err != nil {
		// The error may come from a connection that dropped while the commit went through, in which case
		// the record already names the new key and revoking it would leave the tenant on a dead credential.
		verifyCtx, cancelVerify := context.WithTimeout(context.Background(), rotationCleanupTimeout)
		defer cancelVerify()
		committed, getErr := r.reconcileDAO.GetByTenantID(verifyCtx, db, tenantID)
		switch {
		case getErr != nil:
			// A failed read proves nothing, and a dropped connection makes it likely. Keep the new key and the
			// dirty marker: the sweeper converges on whatever the record names, the new key if the commit went
			// through and the old one if it did not.
			return nil, fmt.Errorf("could not confirm whether the rotation committed, the key sweeper will converge the tenant: %w", err)
		case committed.KeyHash == newHash:
			err = nil
		}
	}
	if err != nil {
		// Nothing points at the new key, so it is an orphan. Revoke it by its own hash; if that fails the
		// dirty marker lets the sweeper do it.
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), rotationCleanupTimeout)
		defer cancelCleanup()
		_ = r.gatewayClient.DeleteKeysByHash(cleanupCtx, []string{newHash})
		return nil, rotationConflict(err)
	}

	result := &RotationResult{TenantID: tenantID, KeyAlias: alias, KeyHash: newHash, RotatedAt: now}
	revokeCtx, cancelRevoke := context.WithTimeout(ctx, rotationGatewayTimeout)
	defer cancelRevoke()
	revoked, err := r.converge(revokeCtx, db, tenantID, newHash)
	result.RevokedKeys = revoked
	result.Converged = err == nil
	if err != nil {
		slog.Warn("key rotation committed but old keys are not revoked yet",
			"tenant_id", tenantID, "operator_id", operator, "error", err)
	}

	slog.Info("tenant gateway key rotated", "tenant_id", tenantID, "operator_id", operator,
		"old_key_hash", rec.KeyHash, "new_key_hash", newHash, "revoked_keys", revoked,
		"converged", result.Converged, "rotated_at", now)
	return result, nil
}

// converge revokes every gateway key of the tenant except keyHash, then clears the dirty marker.
func (r *KeyRotator) converge(ctx context.Context, db *gorm.DB, tenantID, keyHash string) (int, error) {
	revoked, err := ConvergeTenantKeys(ctx, r.gatewayClient, tenantID, keyHash)
	if err != nil {
		return 0, err
	}
	if err := r.reconcileDAO.ClearKeyDirty(ctx, db, tenantID, keyHash); err != nil {
		return revoked, fmt.Errorf("failed to clear the key dirty marker: %w", err)
	}
	return revoked, nil
}

func rotationConflict(err error) error {
	if errors.Is(err, entity.ErrOptimisticLockConflict) {
		return fmt.Errorf("%w: %v", ErrRotationConflict, err)
	}
	return err
}

// findPlatformInstance returns the managed model instance of a tenant.
func findPlatformInstance(ctx context.Context, db *gorm.DB, tenantID string) (*rentity.TenantModelInstance, error) {
	var instance rentity.TenantModelInstance
	err := db.WithContext(ctx).
		Where("instance_name = ?", PlatformInstanceName).
		Where("provider_id IN (?)", db.Model(&rentity.TenantModelProvider{}).
			Select("id").
			Where("tenant_id = ? AND provider_name = ?", tenantID, ProviderOpenAIAPICompatible)).
		First(&instance).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: no managed model instance", ErrTenantNotReady)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the managed instance: %w", err)
	}
	return &instance, nil
}
