package entity

import (
	"time"
)

// PreferenceOrigin defines who chose the default model setting.
type PreferenceOrigin string

const (
	// OriginPlatformDefault indicates the model was assigned automatically by the platform reconciler.
	OriginPlatformDefault PreferenceOrigin = "platform_default"
	// OriginUserExplicit indicates the user deliberately selected this model in UI or API.
	OriginUserExplicit PreferenceOrigin = "user_explicit"
)

// UserDefaultPreference tracks explicit user model choices versus platform-assigned defaults.
// It provides sticky protection for user customizations during global default model upgrades.
type UserDefaultPreference struct {
	ID             string           `gorm:"column:id;primaryKey;size:32" json:"id"`
	TenantID       string           `gorm:"column:tenant_id;size:32;not null;index;uniqueIndex:idx_pref_tenant_user_type,priority:1" json:"tenant_id"`
	UserID         string           `gorm:"column:user_id;size:32;not null;index;uniqueIndex:idx_pref_tenant_user_type,priority:2" json:"user_id"`
	ModelType      int              `gorm:"column:model_type;not null;uniqueIndex:idx_pref_tenant_user_type,priority:3" json:"model_type"`
	Origin         PreferenceOrigin `gorm:"column:origin;size:32;not null;default:'platform_default'" json:"origin"`
	CurrentModelID string           `gorm:"column:current_model_id;size:128;not null" json:"current_model_id"`
	CreatedAt      time.Time        `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time        `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName returns the table name for UserDefaultPreference.
func (UserDefaultPreference) TableName() string {
	return "platform_user_default_preference"
}
