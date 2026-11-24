package entity

import "time"

// TenantUsageDaily stores daily aggregated model consumption per tenant and model.
// Uses a composite primary key (tenant_id, model_name, usage_date) to ensure strict idempotency.
type TenantUsageDaily struct {
	TenantID         string    `gorm:"primaryKey;column:tenant_id;type:varchar(64)" json:"tenant_id"`
	ModelName        string    `gorm:"primaryKey;column:model_name;type:varchar(128)" json:"model_name"`
	UsageDate        string    `gorm:"primaryKey;column:usage_date;type:varchar(10)" json:"usage_date"` // Format: YYYY-MM-DD
	CallCount        int64     `gorm:"column:call_count;default:0" json:"call_count"`
	PromptTokens     int64     `gorm:"column:prompt_tokens;default:0" json:"prompt_tokens"`
	CompletionTokens int64     `gorm:"column:completion_tokens;default:0" json:"completion_tokens"`
	TotalTokens      int64     `gorm:"column:total_tokens;default:0" json:"total_tokens"`
	CachedTokens     int64     `gorm:"column:cached_tokens;default:0" json:"cached_tokens"`
	CacheWriteTokens int64     `gorm:"column:cache_write_tokens;default:0" json:"cache_write_tokens"`
	ReasoningTokens  int64     `gorm:"column:reasoning_tokens;default:0" json:"reasoning_tokens"`
	Characters       int64     `gorm:"column:characters;default:0" json:"characters"`
	TotalSpend       float64   `gorm:"column:total_spend;type:decimal(14,6);default:0.000000" json:"total_spend"`
	PromptSpend      float64   `gorm:"column:prompt_spend;type:decimal(14,6);default:0.000000" json:"prompt_spend"`
	CompletionSpend  float64   `gorm:"column:completion_spend;type:decimal(14,6);default:0.000000" json:"completion_spend"`
	CacheSavings     float64   `gorm:"column:cache_savings;type:decimal(14,6);default:0.000000" json:"cache_savings"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime" json:"updated_at"`
}

// TableName returns the physical table name for GORM.
func (TenantUsageDaily) TableName() string {
	return "platform_tenant_usage_daily"
}
