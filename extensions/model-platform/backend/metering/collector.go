package metering

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SpendLogGatewayClient defines the interface required by SpendLogCollector.
type SpendLogGatewayClient interface {
	GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error)
}

// CollectResult captures the metrics of a spend log collection run.
type CollectResult struct {
	TotalFetched  int       `json:"total_fetched"`
	InsertedCount int       `json:"inserted_count"`
	SkippedCount  int       `json:"skipped_count"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
}

// SpendLogCollector coordinates the pulling of raw gateway spend logs and
// their idempotent insertion into platform_gateway_spend_log.
type SpendLogCollector struct {
	db            *gorm.DB
	gatewayClient SpendLogGatewayClient
	batchSize     int
}

// CollectorOption configures optional SpendLogCollector settings.
type CollectorOption func(*SpendLogCollector)

// WithBatchSize customizes the database insert batch size.
func WithBatchSize(size int) CollectorOption {
	return func(c *SpendLogCollector) {
		if size > 0 {
			c.batchSize = size
		}
	}
}

// NewSpendLogCollector creates a new collector instance.
// Environment variable METERING_COLLECTOR_BATCH_SIZE serves as default, overridden by explicit CollectorOption.
func NewSpendLogCollector(db *gorm.DB, client SpendLogGatewayClient, opts ...CollectorOption) *SpendLogCollector {
	batchSize := 100
	if v := os.Getenv("METERING_COLLECTOR_BATCH_SIZE"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			batchSize = s
		}
	}

	c := &SpendLogCollector{
		db:            db,
		gatewayClient: client,
		batchSize:     batchSize,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// CollectSpendLogs fetches logs from the gateway proxy within [startTime, endTime]
// using paginated requests to /spend/logs/v2 and stores them into the database using physical spend_id deduplication.
func (c *SpendLogCollector) CollectSpendLogs(ctx context.Context, startTime, endTime time.Time) (*CollectResult, error) {
	if c.db == nil {
		return nil, errors.New("database handle cannot be nil")
	}
	if c.gatewayClient == nil {
		return nil, errors.New("gateway client cannot be nil")
	}

	endUTC := endTime.UTC()
	if endUTC.Nanosecond() > 0 {
		endUTC = endUTC.Truncate(time.Second).Add(time.Second)
	}

	batchSize := c.batchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	totalFetched := 0
	insertedTotal := 0
	page := 1

	for {
		params := gateway.SpendLogQueryParams{
			StartDate: startTime.UTC().Format("2006-01-02 15:04:05"),
			EndDate:   endUTC.Format("2006-01-02 15:04:05"),
			Page:      page,
			PageSize:  batchSize,
		}

		entries, err := c.gatewayClient.GetSpendLogs(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch spend logs from gateway (page %d): %w", page, err)
		}

		if len(entries) == 0 {
			break
		}

		totalFetched += len(entries)

		logs := make([]entity.GatewaySpendLog, 0, len(entries))
		for _, entry := range entries {
			logs = append(logs, ConvertEntryToSpendLog(entry))
		}

		// Idempotent insertion using primary key (spend_id)
		res := c.db.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "spend_id"}},
				DoNothing: true,
			}).
			Create(&logs)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to insert spend logs batch (page %d): %w", page, res.Error)
		}
		insertedTotal += int(res.RowsAffected)

		if len(entries) < batchSize || page >= 10000 {
			break
		}
		page++
	}

	skippedCount := totalFetched - insertedTotal
	if skippedCount < 0 {
		skippedCount = 0
	}

	return &CollectResult{
		TotalFetched:  totalFetched,
		InsertedCount: insertedTotal,
		SkippedCount:  skippedCount,
		StartTime:     startTime,
		EndTime:       endTime,
	}, nil
}

// ConvertEntryToSpendLog maps a raw gateway SpendLogEntry into an entity.GatewaySpendLog.
func ConvertEntryToSpendLog(entry gateway.SpendLogEntry) entity.GatewaySpendLog {
	tenantID := ExtractTenantID(entry.TeamID, entry.User, entry.Metadata)

	spendID := entry.SpendID
	if spendID == "" {
		spendID = entry.RequestID
	}
	if spendID == "" {
		// Fallback to deterministic hash if both spend_id and request_id are omitted.
		// Uses JSON array serialization to guarantee unambiguous delimiter-free encoding.
		payload, _ := json.Marshal([]interface{}{
			tenantID,
			entry.APIKey,
			entry.Model,
			entry.StartTime,
			entry.EndTime,
			entry.CallType,
			entry.TotalTokens,
			strconv.FormatFloat(entry.Spend, 'f', -1, 64),
		})
		h := sha256.Sum256(payload)
		spendID = hex.EncodeToString(h[:16])
	}
	keyAlias := ""
	if entry.Metadata != nil {
		if val, ok := entry.Metadata["user_api_key_alias"].(string); ok {
			keyAlias = val
		}
	}

	reqTime := ParseRequestTime(entry.StartTime, entry.EndTime)

	rawPayload := ""
	if len(entry.Metadata) > 0 {
		if b, err := json.Marshal(entry.Metadata); err == nil {
			rawPayload = string(b)
		}
	}

	cachedTokens, cacheWriteTokens, reasoningTokens, promptSpend, completionSpend, cacheSavings := ExtractTokenDetails(entry)

	return entity.GatewaySpendLog{
		SpendID:          spendID,
		TenantID:         tenantID,
		APIKey:           entry.APIKey,
		KeyAlias:         keyAlias,
		ModelName:        entry.Model,
		CallType:         entry.CallType,
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		TotalTokens:      entry.TotalTokens,
		CachedTokens:     cachedTokens,
		CacheWriteTokens: cacheWriteTokens,
		ReasoningTokens:  reasoningTokens,
		Spend:            entry.Spend,
		PromptSpend:      promptSpend,
		CompletionSpend:  completionSpend,
		CacheSavings:     cacheSavings,
		RequestTime:      reqTime,
		RawPayload:       rawPayload,
	}
}

// ExtractTokenDetails normalizes cache and reasoning token metrics and pricing from LiteLLM.
func ExtractTokenDetails(entry gateway.SpendLogEntry) (cachedTokens, cacheWriteTokens, reasoningTokens int, promptSpend, completionSpend, cacheSavings float64) {
	promptSpend = entry.PromptSpend
	completionSpend = entry.CompletionSpend
	cacheSavings = entry.CacheSavings

	if entry.PromptTokensDetails != nil {
		cachedTokens = entry.PromptTokensDetails.CachedTokens
	}
	if cachedTokens == 0 && entry.CacheReadTokens > 0 {
		cachedTokens = entry.CacheReadTokens
	}

	if entry.CacheCreationTokens > 0 {
		cacheWriteTokens = entry.CacheCreationTokens
	}

	if entry.CompletionTokensDetails != nil {
		reasoningTokens = entry.CompletionTokensDetails.ReasoningTokens
	}

	if entry.Metadata != nil {
		if cachedTokens == 0 {
			cachedTokens = extractNestedInt(entry.Metadata, "prompt_tokens_details", "cached_tokens")
			if cachedTokens == 0 {
				cachedTokens = extractNestedInt(entry.Metadata, "usage_object", "prompt_tokens_details", "cached_tokens")
			}
		}

		if cacheWriteTokens == 0 {
			cacheWriteTokens = extractNestedInt(entry.Metadata, "cache_creation_input_tokens")
			if cacheWriteTokens == 0 {
				cacheWriteTokens = extractNestedInt(entry.Metadata, "usage_object", "cache_creation_input_tokens")
			}
		}

		if reasoningTokens == 0 {
			reasoningTokens = extractNestedInt(entry.Metadata, "completion_tokens_details", "reasoning_tokens")
			if reasoningTokens == 0 {
				reasoningTokens = extractNestedInt(entry.Metadata, "usage_object", "completion_tokens_details", "reasoning_tokens")
			}
		}

		if cacheSavings == 0 {
			cacheSavings = extractNestedFloat(entry.Metadata, "saved_cache_cost")
			if cacheSavings == 0 {
				cacheSavings = extractNestedFloat(entry.Metadata, "cache_savings")
			}
		}

		if promptSpend == 0 {
			promptSpend = extractNestedFloat(entry.Metadata, "prompt_spend")
		}
		if completionSpend == 0 {
			completionSpend = extractNestedFloat(entry.Metadata, "completion_spend")
		}
	}

	return
}

func extractNestedInt(m map[string]interface{}, keys ...string) int {
	var current interface{} = m
	for _, k := range keys {
		km, ok := current.(map[string]interface{})
		if !ok || km == nil {
			return 0
		}
		current = km[k]
	}
	switch v := current.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func extractNestedFloat(m map[string]interface{}, keys ...string) float64 {
	var current interface{} = m
	for _, k := range keys {
		km, ok := current.(map[string]interface{})
		if !ok || km == nil {
			return 0
		}
		current = km[k]
	}
	switch v := current.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// ExtractTenantID parses tenant ID from authoritative metadata and team attributes.
// Strictly guards against untrusted client-supplied user string spoofing.
func ExtractTenantID(teamID string, user string, metadata map[string]interface{}) string {
	if teamID != "" {
		return strings.TrimPrefix(teamID, "tenant_")
	}

	if metadata != nil {
		if tid, ok := metadata["tenant_id"].(string); ok && tid != "" {
			return strings.TrimPrefix(tid, "tenant_")
		}
		if tid, ok := metadata["user_api_key_team_id"].(string); ok && tid != "" {
			return strings.TrimPrefix(tid, "tenant_")
		}
		if alias, ok := metadata["user_api_key_alias"].(string); ok && alias != "" {
			if strings.HasPrefix(alias, "tenant_") {
				return strings.TrimPrefix(alias, "tenant_")
			}
		}
	}

	if strings.HasPrefix(user, "tenant_") {
		return strings.TrimPrefix(user, "tenant_")
	}

	return ""
}

// ParseRequestTime parses gateway timestamps supporting multiple ISO/SQL formats.
// Returns UTC Unix epoch (1970-01-01 00:00:00 UTC) as a deterministic sentinel value
// if both timestamps fail to parse, ensuring no time drifting or daily billing pollution.
func ParseRequestTime(startTimeStr, endTimeStr string) time.Time {
	layouts := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}

	for _, timeStr := range []string{startTimeStr, endTimeStr} {
		if strings.TrimSpace(timeStr) == "" {
			continue
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, timeStr); err == nil {
				return t.UTC()
			}
		}
	}

	return time.Unix(0, 0).UTC()
}

// QueryRawSpendLogs retrieves raw spend logs with optional tenant and time range filters.
func QueryRawSpendLogs(ctx context.Context, db *gorm.DB, tenantID string, startTime, endTime *time.Time, limit, offset int) ([]entity.GatewaySpendLog, int64, error) {
	if db == nil {
		return nil, 0, errors.New("database handle cannot be nil")
	}

	query := db.WithContext(ctx).Model(&entity.GatewaySpendLog{})
	if tenantID != "" {
		query = query.Where("tenant_id = ?", tenantID)
	}
	if startTime != nil {
		query = query.Where("request_time >= ?", *startTime)
	}
	if endTime != nil {
		query = query.Where("request_time <= ?", *endTime)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count spend logs: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	var logs []entity.GatewaySpendLog
	err := query.Order("request_time DESC, spend_id DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error
	if err != nil {
		return nil, 0, fmt.Errorf("failed to query spend logs: %w", err)
	}

	return logs, total, nil
}

// parseScannedTime normalizes scanned database time across SQLite (string/bytes), MySQL, and PostgreSQL.
func parseScannedTime(val interface{}) (*time.Time, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case time.Time:
		if v.IsZero() {
			return nil, nil
		}
		t := v.UTC()
		return &t, nil
	case *time.Time:
		if v == nil || v.IsZero() {
			return nil, nil
		}
		t := v.UTC()
		return &t, nil
	case string:
		if v == "" {
			return nil, nil
		}
		layouts := []string{
			"2006-01-02 15:04:05.999999999-07:00",
			"2006-01-02 15:04:05.999999999",
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, v); err == nil {
				t := parsed.UTC()
				return &t, nil
			}
		}
		return nil, fmt.Errorf("unrecognized time format from database: %s", v)
	case []byte:
		return parseScannedTime(string(v))
	default:
		return nil, fmt.Errorf("unexpected database type for time: %T", val)
	}
}

// GetLatestRequestTime queries the maximum request_time currently recorded in platform_gateway_spend_log.
// Returns nil if the table contains zero records (cold start).
func (c *SpendLogCollector) GetLatestRequestTime(ctx context.Context) (*time.Time, error) {
	if c.db == nil {
		return nil, errors.New("database handle cannot be nil")
	}

	row := c.db.WithContext(ctx).
		Model(&entity.GatewaySpendLog{}).
		Select("MAX(request_time)").
		Row()
	if row.Err() != nil {
		return nil, fmt.Errorf("failed to query latest request time: %w", row.Err())
	}

	var raw interface{}
	if err := row.Scan(&raw); err != nil {
		return nil, fmt.Errorf("failed to scan latest request time: %w", err)
	}
	return parseScannedTime(raw)
}

// CollectIncremental performs watermark-driven incremental spend log pulling.
// It derives the start time from MAX(request_time) minus an overlapWindow (default 5m)
// to prevent edge dropping, capped by maxLookback (default 30d).
// If the table is empty (cold start), it pulls from Now() - 48h.
func (c *SpendLogCollector) CollectIncremental(ctx context.Context, overlapWindow, maxLookback time.Duration) (*CollectResult, error) {
	if overlapWindow <= 0 {
		overlapWindow = 5 * time.Minute
	}
	if maxLookback <= 0 {
		maxLookback = 30 * 24 * time.Hour
	}

	latestTime, err := c.GetLatestRequestTime(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var startTime time.Time

	earliestAllowed := now.Add(-maxLookback)
	if latestTime == nil {
		// Cold start: table is completely empty, initialize baseline (48h)
		startTime = now.Add(-48 * time.Hour)
	} else {
		// Incremental: resume from watermark minus safety overlap window
		startTime = latestTime.UTC().Add(-overlapWindow)
	}

	if startTime.Before(earliestAllowed) {
		startTime = earliestAllowed
	}

	if startTime.After(now) {
		startTime = now
	}

	return c.CollectSpendLogs(ctx, startTime, now)
}
