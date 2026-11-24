package entity

import "time"

// GatewaySpendLog represents an immutable raw spend log entry fetched from LiteLLM proxy.
// It uses spend_id as the primary key for strict physical deduplication.
type GatewaySpendLog struct {
	SpendID          string    `gorm:"primaryKey;column:spend_id;type:varchar(128)" json:"spend_id"`
	TenantID         string    `gorm:"column:tenant_id;type:varchar(64);index:idx_spend_tenant_time" json:"tenant_id"`
	APIKey           string    `gorm:"column:api_key;type:varchar(128)" json:"api_key"`
	KeyAlias         string    `gorm:"column:key_alias;type:varchar(128);index:idx_spend_alias" json:"key_alias"`
	ModelName        string    `gorm:"column:model_name;type:varchar(128);index:idx_spend_model" json:"model_name"`
	CallType         string    `gorm:"column:call_type;type:varchar(64)" json:"call_type"`
	PromptTokens     int       `gorm:"column:prompt_tokens;default:0" json:"prompt_tokens"`
	CompletionTokens int       `gorm:"column:completion_tokens;default:0" json:"completion_tokens"`
	TotalTokens      int       `gorm:"column:total_tokens;default:0" json:"total_tokens"`
	CachedTokens     int       `gorm:"column:cached_tokens;default:0" json:"cached_tokens"`
	CacheWriteTokens int       `gorm:"column:cache_write_tokens;default:0" json:"cache_write_tokens"`
	ReasoningTokens  int       `gorm:"column:reasoning_tokens;default:0" json:"reasoning_tokens"`
	Characters       int64     `gorm:"column:characters;default:0" json:"characters"`
	Spend            float64   `gorm:"column:spend;type:decimal(14,6);default:0.000000" json:"spend"`
	PromptSpend      float64   `gorm:"column:prompt_spend;type:decimal(14,6);default:0.000000" json:"prompt_spend"`
	CompletionSpend  float64   `gorm:"column:completion_spend;type:decimal(14,6);default:0.000000" json:"completion_spend"`
	CacheSavings     float64   `gorm:"column:cache_savings;type:decimal(14,6);default:0.000000" json:"cache_savings"`
	RequestTime      time.Time `gorm:"column:request_time;index:idx_spend_tenant_time;index:idx_spend_time" json:"request_time"`
	RawPayload       string    `gorm:"column:raw_payload;type:text" json:"raw_payload,omitempty"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime" json:"created_at"`
}

// TableName returns the physical table name for GORM.
func (GatewaySpendLog) TableName() string {
	return "platform_gateway_spend_log"
}
