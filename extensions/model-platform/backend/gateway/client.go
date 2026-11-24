package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client defines the interface for managing LiteLLM virtual keys and proxy communication.
type Client interface {
	GenerateKey(ctx context.Context, req *GenerateKeyRequest) (*GenerateKeyResponse, error)
	UpdateKey(ctx context.Context, req *UpdateKeyRequest) (*UpdateKeyResponse, error)
	DeleteKeyByAlias(ctx context.Context, alias string) error
	GetKeyInfo(ctx context.Context, key string) (*KeyInfoResponse, error)
	GetSpendLogs(ctx context.Context, params SpendLogQueryParams) ([]SpendLogEntry, error)
	HealthCheck(ctx context.Context) error
}

type litellmClient struct {
	config     GatewayConfig
	httpClient *http.Client
}

// NewClient creates a production-grade LiteLLM gateway client with strict network timeout controls.
func NewClient(config GatewayConfig) Client {
	if config.Timeout <= 0 {
		config.Timeout = 10 * time.Second
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 3 * time.Second
	}
	if config.MaxIdleConns <= 0 {
		config.MaxIdleConns = 50
	}
	if config.IdleConnTimeout <= 0 {
		config.IdleConnTimeout = 30 * time.Second
	}

	maxIdleConnsPerHost := config.MaxIdleConns

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   config.ConnectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   2 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}

	return &litellmClient{
		config:     config,
		httpClient: httpClient,
	}
}

// GenerateKey requests LiteLLM to generate a tenant-scoped virtual key with bounded models.
func (c *litellmClient) GenerateKey(ctx context.Context, req *GenerateKeyRequest) (*GenerateKeyResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("generate key request cannot be nil")
	}
	if len(req.Models) == 0 {
		return nil, fmt.Errorf("models list cannot be empty for tenant virtual key")
	}
	if req.KeyAlias == "" && req.KeyName != "" {
		req.KeyAlias = req.KeyName
	}

	var resp GenerateKeyResponse
	if err := c.doRequest(ctx, http.MethodPost, "/key/generate", req, &resp); err != nil {
		return nil, fmt.Errorf("failed to generate virtual key: %w", err)
	}

	if resp.KeyName == "" && resp.KeyAlias != "" {
		resp.KeyName = resp.KeyAlias
	}
	if resp.KeyAlias == "" && resp.KeyName != "" {
		resp.KeyAlias = resp.KeyName
	}

	return &resp, nil
}

// UpdateKey updates configuration, model whitelist, or budgets on an existing virtual key.
func (c *litellmClient) UpdateKey(ctx context.Context, req *UpdateKeyRequest) (*UpdateKeyResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("update key request cannot be nil")
	}
	if req.Key == "" {
		return nil, fmt.Errorf("key identifier is required for update")
	}

	var resp UpdateKeyResponse
	if err := c.doRequest(ctx, http.MethodPost, "/key/update", req, &resp); err != nil {
		return nil, fmt.Errorf("failed to update virtual key: %w", err)
	}

	return &resp, nil
}

// DeleteKeyByAlias revokes the virtual key identified by its unique key_alias from the LiteLLM proxy.
func (c *litellmClient) DeleteKeyByAlias(ctx context.Context, alias string) error {
	if alias == "" {
		return fmt.Errorf("key alias cannot be empty")
	}

	req := DeleteKeyRequest{KeyAliases: []string{alias}}
	if err := c.doRequest(ctx, http.MethodPost, "/key/delete", req, nil); err != nil {
		return fmt.Errorf("failed to delete virtual key by alias: %w", err)
	}

	return nil
}

// GetKeyInfo retrieves metadata and usage metrics for a given virtual key.
func (c *litellmClient) GetKeyInfo(ctx context.Context, key string) (*KeyInfoResponse, error) {
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}

	endpoint := fmt.Sprintf("/key/info?key=%s", url.QueryEscape(key))
	var resp KeyInfoResponse
	if err := c.doRequest(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
		return nil, fmt.Errorf("failed to get key info: %w", err)
	}

	return &resp, nil
}

// GetSpendLogs retrieves raw invocation spend logs from the LiteLLM proxy within a given date range or filter.
// Queries the paginated individual-record endpoint /spend/logs/v2 available in LiteLLM v1.110.0+.
func (c *litellmClient) GetSpendLogs(ctx context.Context, params SpendLogQueryParams) ([]SpendLogEntry, error) {
	endpoint := "/spend/logs/v2"
	queryVals := url.Values{}
	if params.StartDate != "" {
		queryVals.Set("start_date", params.StartDate)
	}
	if params.EndDate != "" {
		queryVals.Set("end_date", params.EndDate)
	}
	if params.APIKey != "" {
		queryVals.Set("api_key", params.APIKey)
	}
	if params.UserID != "" {
		queryVals.Set("user_id", params.UserID)
	}
	if params.Page > 0 {
		queryVals.Set("page", strconv.Itoa(params.Page))
	}
	if params.PageSize > 0 {
		queryVals.Set("page_size", strconv.Itoa(params.PageSize))
	}
	if len(queryVals) > 0 {
		endpoint += "?" + queryVals.Encode()
	}

	var raw json.RawMessage
	if err := c.doRequest(ctx, http.MethodGet, endpoint, nil, &raw); err != nil {
		return nil, fmt.Errorf("failed to get spend logs: %w", err)
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []SpendLogEntry{}, nil
	}

	if trimmed[0] == '[' {
		var list []SpendLogEntry
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("failed to unmarshal spend logs array: %w", err)
		}
		return list, nil
	}

	// Support wrapper object {"data": [...]} or {"spend_logs": [...]}
	var wrapper struct {
		Data      []SpendLogEntry `json:"data"`
		SpendLogs []SpendLogEntry `json:"spend_logs"`
	}
	if err := json.Unmarshal(trimmed, &wrapper); err != nil {
		return nil, fmt.Errorf("failed to unmarshal spend logs wrapper: %w", err)
	}
	if len(wrapper.Data) > 0 {
		return wrapper.Data, nil
	}
	if len(wrapper.SpendLogs) > 0 {
		return wrapper.SpendLogs, nil
	}
	return []SpendLogEntry{}, nil
}

// HealthCheck verifies connectivity and liveliness of the LiteLLM Proxy.
// Drains the response body to ensure underlying HTTP keep-alive connection reuse.
func (c *litellmClient) HealthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL("/health/liveliness"), nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("gateway health check failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read health check response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gateway unhealthy: status code %d, message: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	return nil
}

func (c *litellmClient) doRequest(ctx context.Context, method, endpoint string, bodyObj interface{}, resultObj interface{}) error {
	fullURL := c.buildURL(endpoint)

	var bodyReader io.Reader
	if bodyObj != nil {
		data, err := json.Marshal(bodyObj)
		if err != nil {
			return fmt.Errorf("failed to marshal request payload: %w", err)
		}
		bodyReader = bytes.NewBuffer(data)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create http request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.config.MasterKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.config.MasterKey)
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request to gateway failed: %w", sanitizeURLError(err))
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("failed to read gateway response: %w", err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return fmt.Errorf("gateway returned error status %d: %s", httpResp.StatusCode, string(respBytes))
	}

	if resultObj != nil && len(respBytes) > 0 {
		if err := json.Unmarshal(respBytes, resultObj); err != nil {
			return fmt.Errorf("failed to unmarshal gateway response: %w", err)
		}
	}

	return nil
}

func (c *litellmClient) buildURL(endpoint string) string {
	base := strings.TrimRight(c.config.BaseURL, "/")
	path := strings.TrimLeft(endpoint, "/")
	return base + "/" + path
}

func sanitizeURLError(err error) error {
	if urlErr, ok := err.(*url.Error); ok {
		if parsedURL, parseErr := url.Parse(urlErr.URL); parseErr == nil && parsedURL.RawQuery != "" {
			parsedURL.RawQuery = ""
			urlErr.URL = parsedURL.String()
		}
	}
	return err
}

// MaskKey redacts a virtual key for safe logging, exposing only non-sensitive prefixes and suffixes.
func MaskKey(key string) string {
	if len(key) == 0 {
		return "****"
	}
	if strings.HasPrefix(key, "sk-") {
		if len(key) <= 12 {
			return "sk-****"
		}
		prefix := key[:7] // e.g. "sk-lite"
		suffix := key[len(key)-4:]
		return prefix + "..." + suffix
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "..." + key[len(key)-3:]
}
