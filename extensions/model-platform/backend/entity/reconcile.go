package entity

import (
	"errors"
	"fmt"
	"time"
)

// ReconcileStatus represents the lifecycle state of a tenant in the model platform.
type ReconcileStatus string

const (
	// StatusPending indicates the tenant has been created in DB but platform provisioning hasn't started.
	StatusPending ReconcileStatus = "pending"
	// StatusProvisioning indicates virtual key generation and model assignment is in progress.
	StatusProvisioning ReconcileStatus = "provisioning"
	// StatusReady indicates the tenant has been successfully provisioned with virtual key and default models.
	StatusReady ReconcileStatus = "ready"
	// StatusDegraded indicates provisioning failed transiently and is scheduled for level-triggered retry.
	StatusDegraded ReconcileStatus = "degraded"
	// StatusFailed indicates max retry attempts have been exhausted and manual intervention is needed.
	StatusFailed ReconcileStatus = "failed"
	// StatusDeleted indicates the tenant was removed and its gateway virtual key has been revoked.
	StatusDeleted ReconcileStatus = "deleted"
)

var (
	// ErrInvalidStateTransition is returned when an illegal status change is attempted.
	ErrInvalidStateTransition = errors.New("invalid state transition")
	// ErrOptimisticLockConflict is returned when an update fails due to version mismatch.
	ErrOptimisticLockConflict = errors.New("optimistic lock conflict: record version mismatch")
	// ErrRecordNotFound is returned when a reconcile record does not exist.
	ErrRecordNotFound = errors.New("tenant reconcile record not found")
)

var validTransitions = map[ReconcileStatus]map[ReconcileStatus]bool{
	StatusPending: {
		StatusProvisioning: true,
		StatusDeleted:      true,
	},
	StatusProvisioning: {
		StatusReady:    true,
		StatusDegraded: true,
		StatusFailed:   true,
		StatusDeleted:  true,
	},
	StatusReady: {
		StatusProvisioning: true, // re-provisioning or updating model assignments
		StatusDeleted:      true,
	},
	StatusDegraded: {
		StatusProvisioning: true, // retry attempt triggered
		StatusFailed:       true, // exhausted max retries or permanent failure
		StatusDeleted:      true,
	},
	StatusFailed: {
		StatusProvisioning: true, // manual retry intervention
		StatusDeleted:      true,
	},
	StatusDeleted: {}, // Terminal state: no outbound transitions permitted
}

// IsValidTransition returns true if transitioning from 'from' to 'to' is legally permitted.
func IsValidTransition(from, to ReconcileStatus) bool {
	allowed, ok := validTransitions[from]
	if !ok {
		return false
	}
	if from == to {
		return from != StatusProvisioning // No-op idempotent transitions are valid (except transient in-progress states)
	}
	return allowed[to]
}

// ValidateTransition returns an error if transitioning from 'from' to 'to' is illegal.
func ValidateTransition(from, to ReconcileStatus) error {
	if !IsValidTransition(from, to) {
		return fmt.Errorf("%w: cannot transition from %q to %q", ErrInvalidStateTransition, from, to)
	}
	return nil
}

// TenantReconcileRecord tracks the asynchronous provisioning state of a tenant on the model platform.
type TenantReconcileRecord struct {
	TenantID        string          `gorm:"column:tenant_id;primaryKey;size:32" json:"tenant_id"`
	Status          ReconcileStatus `gorm:"column:status;size:32;not null;default:'pending';index:idx_status_retry,priority:1" json:"status"`
	RetryCount      int             `gorm:"column:retry_count;not null;default:0" json:"retry_count"`
	MaxRetries      int             `gorm:"column:max_retries;not null;default:50" json:"max_retries"`
	NextRetryAt     *time.Time      `gorm:"column:next_retry_at;index:idx_status_retry,priority:2" json:"next_retry_at,omitempty"`
	LastError       string          `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	GatewayKeyAlias string          `gorm:"column:gateway_key_alias;size:128" json:"gateway_key_alias"`
	KeyHash         string          `gorm:"column:key_hash;size:128" json:"key_hash"`
	// KeyDirtySince marks that the gateway may hold keys of the tenant other than the one named by KeyHash,
	// because a rotation started and has not converged yet. It is set before a new key is issued and only
	// cleared once every other key of the tenant's gateway team is revoked.
	KeyDirtySince *time.Time `gorm:"column:key_dirty_since;index:idx_key_dirty" json:"key_dirty_since,omitempty"`
	KeyRotatedAt  *time.Time `gorm:"column:key_rotated_at" json:"key_rotated_at,omitempty"`
	KeyRotatedBy  string     `gorm:"column:key_rotated_by;size:128" json:"key_rotated_by,omitempty"`
	Version       int64      `gorm:"column:version;not null;default:1" json:"version"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName returns the physical table name for TenantReconcileRecord.
func (TenantReconcileRecord) TableName() string {
	return "platform_tenant_reconcile"
}
