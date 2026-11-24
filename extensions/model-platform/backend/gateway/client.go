package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// StatusError is returned when the gateway answers with a non-2xx status.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gateway returned error status %d: %s", e.Code, e.Body)
}

// Client defines the interface for managing LiteLLM virtual keys and proxy communication.
type Client interface {
	GenerateKey(ctx context.Context, req *GenerateKeyRequest) (*GenerateKeyResponse, error)
	UpdateKey(ctx context.Context, req *UpdateKeyRequest) (*UpdateKeyResponse, error)
	DeleteKeysByHash(ctx context.Context, hashes []string) error
	ListTeamKeys(ctx context.Context, teamID string) ([]TeamKey, error)
	GetKeyInfo(ctx context.Context, key string) (*KeyInfoResponse, error)
	ListModels(ctx context.Context) ([]string, error)
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
	if req.TeamID != "" {
		if err := c.ensureTeam(ctx, req.TeamID); err != nil {
			return nil, fmt.Errorf("failed to ensure the gateway team: %w", err)
		}
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

// ensureTeam creates the LiteLLM team that groups the keys of a tenant. LiteLLM accepts a team_id on
// /key/generate without checking that the team exists, but /key/update then fails with "Team doesn't exist
// in db", which would make every later whitelist change of the key fail. An existing team is not an error.
func (c *litellmClient) ensureTeam(ctx context.Context, teamID string) error {
	err := c.doRequest(ctx, http.MethodPost, "/team/new", TeamRequest{TeamID: teamID, TeamAlias: teamID}, nil)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
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

// DeleteKeysByHash revokes virtual keys addressed by the sha256 hex digest of the key. It is idempotent:
// LiteLLM answers 404 "No keys found" only when none of the keys exists, which means they are all gone, so a
// retry after a partial failure is always safe. Any other 404, such as a wrong route in front of the proxy,
// is reported, because treating it as success would claim a revocation that did not happen.
func (c *litellmClient) DeleteKeysByHash(ctx context.Context, hashes []string) error {
	if len(hashes) == 0 {
		return nil
	}
	for _, h := range hashes {
		if !isKeyHash(h) {
			return fmt.Errorf("key hash must be a sha256 hex digest")
		}
	}

	err := c.doRequest(ctx, http.MethodPost, "/key/delete", DeleteKeyRequest{Keys: hashes}, nil)
	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.Code == http.StatusNotFound && strings.Contains(statusErr.Body, "No keys found") {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to delete virtual keys: %w", err)
	}
	return nil
}

// ListTeamKeys returns every virtual key that belongs to the gateway team, following pagination.
func (c *litellmClient) ListTeamKeys(ctx context.Context, teamID string) ([]TeamKey, error) {
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}

	var keys []TeamKey
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("/key/list?team_id=%s&return_full_object=true&page=%d&size=%d",
			url.QueryEscape(teamID), page, listKeysPageSize)
		var resp ListKeysResponse
		if err := c.doRequest(ctx, http.MethodGet, endpoint, nil, &resp); err != nil {
			return nil, fmt.Errorf("failed to list team keys: %w", err)
		}
		keys = append(keys, resp.Keys...)
		if page >= resp.TotalPages {
			return keys, nil
		}
	}
}

// listKeysPageSize is the largest page LiteLLM /key/list accepts.
const listKeysPageSize = 100

// HashKey returns the gateway token identifier of a virtual key: its sha256 hex digest. The same value
// is the api_key of the key's spend logs and the token LiteLLM stores, so it is safe to persist and log.
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func isKeyHash(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
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

// ListModels returns the model names currently routable through the gateway (GET /v1/models).
func (c *litellmClient) ListModels(ctx context.Context) ([]string, error) {
	var resp ListModelsResponse
	if err := c.doRequest(ctx, http.MethodGet, "/v1/models", nil, &resp); err != nil {
		return nil, fmt.Errorf("failed to list gateway models: %w", err)
	}

	names := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		names = append(names, m.ID)
	}
	return names, nil
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
		return &StatusError{Code: httpResp.StatusCode, Body: string(respBytes)}
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
