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
	Spend     float64                `json:"spend,omitempty"`
	MaxBudget *float64               `json:"max_budget,omitempty"`
	Expires   string                 `json:"expires,omitempty"`
	Models    []string               `json:"models,omitempty"`
	UserID    string                 `json:"user_id,omitempty"`
	TeamID    string                 `json:"team_id,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}
