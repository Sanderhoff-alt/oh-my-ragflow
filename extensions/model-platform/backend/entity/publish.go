package entity

import "time"

// Catalog status values of PlatformManagedModel. The catalog only records the administrator's intent;
// deprecated and tombstoned are per-tenant states derived from it (see tenant_model extra flags).
const (
	ManagedModelStatusActive  = "active"
	ManagedModelStatusRetired = "retired"
)

// PlatformManagedModel is the administrator-owned catalog of platform managed models.
// model_name is unique because the gateway routes and the virtual key whitelists are keyed by name.
type PlatformManagedModel struct {
	ID         string  `gorm:"column:id;primaryKey;size:32" json:"id"`
	ModelName  string  `gorm:"column:model_name;size:128;not null;uniqueIndex:idx_managed_model_name" json:"model_name"`
	ModelType  int     `gorm:"column:model_type;not null;index" json:"model_type"` // bit flags of ragflow/internal/entity.ModelType
	IsDefault  bool    `gorm:"column:is_default;not null;default:false;index" json:"is_default"`
	Status     string  `gorm:"column:status;size:32;not null;default:'active';index" json:"status"`
	Extra      *string `gorm:"column:extra;type:text" json:"extra,omitempty"`
	CreateTime int64   `gorm:"column:create_time;autoCreateTime:milli" json:"create_time"`
	UpdateTime int64   `gorm:"column:update_time;autoUpdateTime:milli" json:"update_time"`
}

// TableName returns the physical table name for PlatformManagedModel.
func (PlatformManagedModel) TableName() string {
	return "platform_managed_model"
}

// ModelPublishTask records one onboarding of a catalog model to all tenants.
// Its status is not stored: it is derived from the items so it cannot drift.
type ModelPublishTask struct {
	ID        string    `gorm:"column:id;primaryKey;size:32" json:"id"`
	ModelName string    `gorm:"column:model_name;size:128;not null;index" json:"model_name"`
	ModelType int       `gorm:"column:model_type;not null" json:"model_type"`
	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime;index" json:"created_at"`
}

// TableName returns the physical table name for ModelPublishTask.
func (ModelPublishTask) TableName() string {
	return "platform_model_publish_task"
}

// PublishItemStatus is the state of one tenant inside a publish task.
type PublishItemStatus string

const (
	// PublishItemPending waits for a worker, possibly until NextRetryAt.
	PublishItemPending PublishItemStatus = "pending"
	// PublishItemRunning is claimed by a worker; a stale lease returns it to pending.
	PublishItemRunning PublishItemStatus = "running"
	// PublishItemSucceeded means the gateway whitelist and tenant_model are both updated.
	PublishItemSucceeded PublishItemStatus = "succeeded"
	// PublishItemFailed exhausted its retries and waits for a manual retry.
	PublishItemFailed PublishItemStatus = "failed"
)

// ModelPublishItem tracks the rollout of one model to one tenant.
type ModelPublishItem struct {
	TaskID      string            `gorm:"column:task_id;primaryKey;size:32" json:"task_id"`
	TenantID    string            `gorm:"column:tenant_id;primaryKey;size:32" json:"tenant_id"`
	Status      PublishItemStatus `gorm:"column:status;size:16;not null;default:'pending';index:idx_publish_item_due,priority:1" json:"status"`
	RetryCount  int               `gorm:"column:retry_count;not null;default:0" json:"retry_count"`
	NextRetryAt *time.Time        `gorm:"column:next_retry_at;index:idx_publish_item_due,priority:2" json:"next_retry_at,omitempty"`
	LastError   string            `gorm:"column:last_error;type:text" json:"last_error,omitempty"`
	Version     int64             `gorm:"column:version;not null;default:1" json:"version"`
	// UpgradedCount and PreservedCount are the users StickyGuard moved to the platform default in this tenant
	// and the users whose explicit choice it kept. They stay zero unless the model is the platform default.
	UpgradedCount  int64     `gorm:"column:upgraded_count;not null;default:0" json:"upgraded_count"`
	PreservedCount int64     `gorm:"column:preserved_count;not null;default:0" json:"preserved_count"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName returns the physical table name for ModelPublishItem.
func (ModelPublishItem) TableName() string {
	return "platform_model_publish_item"
}
