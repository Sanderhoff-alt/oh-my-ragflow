package entity

import "time"

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

// TenantReconcileRecord tracks the asynchronous provisioning state of a tenant on the model platform.
type TenantReconcileRecord struct {
	TenantID        string          `gorm:"column:tenant_id;primaryKey;size:32" json:"tenant_id"`
	Status          ReconcileStatus `gorm:"column:status;size:32;not null;default:'pending';index" json:"status"`
	RetryCount      int             `gorm:"column:retry_count;not null;default:0" json:"retry_count"`
	MaxRetries      int             `gorm:"column:max_retries;not null;default:50" json:"max_retries"`
	NextRetryAt     *time.Time      `gorm:"column:next_retry_at;index" json:"next_retry_at,omitempty"`
	GatewayKeyAlias string          `gorm:"column:gateway_key_alias;size:128" json:"gateway_key_alias"`
	KeyHash         string          `gorm:"column:key_hash;size:64" json:"key_hash"`
	Version         int64           `gorm:"column:version;not null;default:1" json:"version"`
	CreatedAt       time.Time       `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt       time.Time       `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName returns the physical table name for TenantReconcileRecord.
func (TenantReconcileRecord) TableName() string {
	return "platform_tenant_reconcile"
}
