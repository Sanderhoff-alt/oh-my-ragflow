package gateway

import (
	"time"
)

// GatewayConfig holds configuration for communicating with the LiteLLM Proxy.
type GatewayConfig struct {
	BaseURL         string        `json:"base_url"`
	MasterKey       string        `json:"master_key"`
	Timeout         time.Duration `json:"timeout"`
	ConnectTimeout  time.Duration `json:"connect_timeout"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	IdleConnTimeout time.Duration `json:"idle_conn_timeout"`
}

// GenerateKeyRequest represents payload for LiteLLM /key/generate endpoint.
type GenerateKeyRequest struct {
	KeyAlias  string                 `json:"key_alias,omitempty"`
	KeyName   string                 `json:"key_name,omitempty"`
	Models    []string               `json:"models,omitempty"`
	Duration  string                 `json:"duration,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	TeamID    string                 `json:"team_id,omitempty"`
	RPMLimit  *int                   `json:"rpm_limit,omitempty"`
	TPMLimit  *int                   `json:"tpm_limit,omitempty"`
}

// GenerateKeyResponse represents response from LiteLLM /key/generate endpoint.
type GenerateKeyResponse struct {
	Key       string                 `json:"key"`
	Token     string                 `json:"token,omitempty"`
	KeyAlias  string                 `json:"key_alias,omitempty"`
	KeyName   string                 `json:"key_name,omitempty"`
	Expires   string                 `json:"expires,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	Models    []string               `json:"models,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Spend     float64                `json:"spend,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// UpdateKeyRequest represents payload for LiteLLM /key/update endpoint.
type UpdateKeyRequest struct {
	Key       string                 `json:"key"`
	Models    []string               `json:"models,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Duration  string                 `json:"duration,omitempty"`
}

// UpdateKeyResponse represents response from LiteLLM /key/update endpoint.
type UpdateKeyResponse struct {
	Key       string                 `json:"key,omitempty"`
	KeyAlias  string                 `json:"key_alias,omitempty"`
	KeyName   string                 `json:"key_name,omitempty"`
	Models    []string               `json:"models,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// DeleteKeyRequest represents payload for LiteLLM /key/delete endpoint.
type DeleteKeyRequest struct {
	Keys []string `json:"keys"`
}

// DeleteKeyResponse represents response from LiteLLM /key/delete endpoint.
type DeleteKeyResponse struct {
	DeletedKeys []string `json:"deleted_keys,omitempty"`
	Detail      string   `json:"detail,omitempty"`
}

// KeyInfoResponse represents response from LiteLLM /key/info endpoint.
type KeyInfoResponse struct {
	Key  string  `json:"key,omitempty"`
	Info KeyInfo `json:"info"`
}

// KeyInfo contains detailed information about a LiteLLM virtual key.
type KeyInfo struct {
	Token     string                 `json:"token,omitempty"`
	KeyAlias  string                 `json:"key_alias,omitempty"`
	KeyName   string                 `json:"key_name,omitempty"`
	Status    string                 `json:"status,omitempty"`
	Spend     float64                `json:"spend,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Expires   string                 `json:"expires,omitempty"`
	Models    []string               `json:"models,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	TeamID    string                 `json:"team_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// ListModelsResponse represents response from the OpenAI-compatible LiteLLM /v1/models endpoint.
type ListModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// SpendLogQueryParams defines filter and pagination parameters for querying LiteLLM spend logs.
type SpendLogQueryParams struct {
	StartDate string `json:"start_date,omitempty"` // Format: YYYY-MM-DD or RFC3339
	EndDate   string `json:"end_date,omitempty"`   // Format: YYYY-MM-DD or RFC3339
	APIKey    string `json:"api_key,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Page      int    `json:"page,omitempty"`
	PageSize  int    `json:"page_size,omitempty"`
}

// PromptTokensDetails holds normalized prompt cache metrics adhering to OpenAI standard.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// CompletionTokensDetails holds normalized reasoning metrics adhering to OpenAI standard.
type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// SpendLogEntry represents an individual raw invocation record returned by LiteLLM /spend/logs/v2.
type SpendLogEntry struct {
	RequestID               string                   `json:"request_id,omitempty"`
	SpendID                 string                   `json:"spend_id,omitempty"`
	APIKey                  string                   `json:"api_key,omitempty"`
	User                    string                   `json:"user,omitempty"`
	TeamID                  string                   `json:"team_id,omitempty"`
	Model                   string                   `json:"model,omitempty"`
	CallType                string                   `json:"call_type,omitempty"`
	Spend                   float64                  `json:"spend,omitempty"`
	PromptSpend             float64                  `json:"prompt_spend,omitempty"`
	CompletionSpend         float64                  `json:"completion_spend,omitempty"`
	CacheSavings            float64                  `json:"cache_savings,omitempty"`
	TotalTokens             int                      `json:"total_tokens,omitempty"`
	PromptTokens            int                      `json:"prompt_tokens,omitempty"`
	CompletionTokens        int                      `json:"completion_tokens,omitempty"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	CacheCreationTokens     int                      `json:"cache_creation_input_tokens,omitempty"`
	CacheReadTokens         int                      `json:"cache_read_input_tokens,omitempty"`
	StartTime               string                   `json:"startTime,omitempty"`
	EndTime                 string                   `json:"endTime,omitempty"`
	Metadata                map[string]interface{}   `json:"metadata,omitempty"`
}
