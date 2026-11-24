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
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultOverlapWindow = 5 * time.Minute

// SpendLogGatewayClient defines the interface required by SpendLogCollector.
type SpendLogGatewayClient interface {
	GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error)
}

// CollectResult captures the metrics of a spend log collection run.
type CollectResult struct {
	TotalFetched       int       `json:"total_fetched"`
	InsertedCount      int       `json:"inserted_count"`
	SkippedCount       int       `json:"skipped_count"`
	StartTime          time.Time `json:"start_time"`
	EndTime            time.Time `json:"end_time"`
	EarliestInsertedAt time.Time `json:"earliest_inserted_at,omitempty"`
}

// SpendLogCollector coordinates the pulling of raw gateway spend logs and
// their idempotent insertion into platform_gateway_spend_log.
type SpendLogCollector struct {
	db            *gorm.DB
	gatewayClient SpendLogGatewayClient
	batchSize     int
	nowFunc       func() time.Time

	collectMu          sync.Mutex
	mu                 sync.RWMutex
	loc                *time.Location
	lastCheckedAt      time.Time
	pendingReplayStart time.Time
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

// WithCollectorTimezone sets the calendar-day alignment timezone for cold-start collection.
func WithCollectorTimezone(loc *time.Location) CollectorOption {
	return func(c *SpendLogCollector) {
		if loc != nil {
			c.loc = loc
		}
	}
}

// NewSpendLogCollector creates a new collector instance.
// Environment variables METERING_COLLECTOR_BATCH_SIZE and METERING_TIMEZONE serve as defaults,
// overridden by explicit CollectorOptions.
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
		loc:           ResolveMeteringLocation(""),
		nowFunc:       time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Location returns the configured calendar-day alignment timezone.
func (c *SpendLogCollector) Location() *time.Location {
	c.mu.RLock()
	loc := c.loc
	c.mu.RUnlock()
	if loc == nil {
		return time.UTC
	}
	return loc
}

// SetLocation updates the calendar-day alignment timezone on the collector.
func (c *SpendLogCollector) SetLocation(loc *time.Location) {
	if loc == nil {
		return
	}
	c.mu.Lock()
	c.loc = loc
	c.mu.Unlock()
}

func (c *SpendLogCollector) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
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
	skippedTotal := 0
	var earliestInsertedAt time.Time
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
			return partialCollectResult(totalFetched, insertedTotal, skippedTotal, startTime, endTime, earliestInsertedAt), fmt.Errorf("failed to fetch spend logs from gateway (page %d): %w", page, err)
		}

		if len(entries) == 0 {
			break
		}

		totalFetched += len(entries)

		logs := make([]entity.GatewaySpendLog, 0, len(entries))
		for _, entry := range entries {
			logs = append(logs, ConvertEntryToSpendLog(entry))
		}

		// Identify which spend_ids in this batch are genuinely new before inserting so
		// EarliestInsertedAt reflects only newly inserted rows, never pre-existing rows
		// that happened to fall inside the overlap/replay window.
		spendIDs := make([]string, len(logs))
		for i, l := range logs {
			spendIDs[i] = l.SpendID
		}
		var existingIDs []string
		if err := c.db.WithContext(ctx).
			Model(&entity.GatewaySpendLog{}).
			Where("spend_id IN ?", spendIDs).
			Pluck("spend_id", &existingIDs).Error; err != nil {
			return partialCollectResult(totalFetched, insertedTotal, skippedTotal, startTime, endTime, earliestInsertedAt), fmt.Errorf("failed to check existing spend logs batch (page %d): %w", page, err)
		}
		existingSet := make(map[string]struct{}, len(existingIDs))
		for _, id := range existingIDs {
			existingSet[id] = struct{}{}
		}

		// Idempotent insertion using primary key (spend_id)
		res := c.db.WithContext(ctx).
			Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "spend_id"}},
				DoNothing: true,
			}).
			Create(&logs)
		if res.Error != nil {
			return partialCollectResult(totalFetched, insertedTotal, skippedTotal, startTime, endTime, earliestInsertedAt), fmt.Errorf("failed to insert spend logs batch (page %d): %w", page, res.Error)
		}
		inserted := int(res.RowsAffected)
		insertedTotal += inserted
		if batchSkipped := len(entries) - inserted; batchSkipped > 0 {
			skippedTotal += batchSkipped
		}
		if inserted > 0 {
			for _, l := range logs {
				if _, existed := existingSet[l.SpendID]; existed {
					continue
				}
				if l.RequestTime.Unix() > 0 && (earliestInsertedAt.IsZero() || l.RequestTime.Before(earliestInsertedAt)) {
					earliestInsertedAt = l.RequestTime
				}
			}
		}

		if len(entries) < batchSize {
			break
		}
		page++
	}

	return partialCollectResult(totalFetched, insertedTotal, skippedTotal, startTime, endTime, earliestInsertedAt), nil
}

func partialCollectResult(totalFetched, insertedTotal, skippedTotal int, startTime, endTime, earliestInsertedAt time.Time) *CollectResult {
	return &CollectResult{
		TotalFetched:       totalFetched,
		InsertedCount:      insertedTotal,
		SkippedCount:       skippedTotal,
		StartTime:          startTime,
		EndTime:            endTime,
		EarliestInsertedAt: earliestInsertedAt,
	}
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
	characters := ExtractCharacters(entry)

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
		Characters:       characters,
		Spend:            entry.Spend,
		PromptSpend:      promptSpend,
		CompletionSpend:  completionSpend,
		CacheSavings:     cacheSavings,
		RequestTime:      reqTime,
		RawPayload:       rawPayload,
	}
}

// ExtractCharacters extracts character-based usage metrics for TTS speech synthesis calls.
// It inspects known LiteLLM metadata fields first and falls back to prompt/total tokens when
// call_type identifies a speech synthesis call (e.g. speech, aspeech, audio_speech).
func ExtractCharacters(entry gateway.SpendLogEntry) int64 {
	if entry.Metadata != nil {
		for _, path := range [][]string{
			{"characters"},
			{"input_characters"},
			{"additional_usage_values", "characters"},
			{"usage_object", "characters"},
			{"usage_object", "input_characters"},
		} {
			if n := extractNestedInt(entry.Metadata, path...); n > 0 {
				return int64(n)
			}
		}
	}

	callType := strings.ToLower(strings.TrimSpace(entry.CallType))
	if callType == "speech" || callType == "aspeech" || strings.Contains(callType, "speech") || callType == "tts" {
		if entry.PromptTokens > 0 {
			return int64(entry.PromptTokens)
		}
		if entry.TotalTokens > 0 {
			return int64(entry.TotalTokens)
		}
	}
	return 0
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
// The key alias is deliberately not a source: it is a free-form label (rotated keys carry a random
// suffix), while the gateway team id is always set to the tenant id when a key is issued.
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

// CollectIncremental performs self-healing watermark-driven incremental spend log pulling:
//   - Online operation (in-memory checkpoint active): pulls [lastCheckedAt - overlapWindow, now]
//     so idle periods stay strictly bounded without window dilation. If the wall clock moves
//     backward behind lastCheckedAt, it replays [now - overlapWindow, now] and rebases the
//     checkpoint on success.
//   - Restart recovery (checkpoint unset, DB has records): resumes from MAX(request_time) - overlapWindow
//     with no lookback cap, streaming in batches to guarantee zero data loss.
//   - Cold start (checkpoint unset, DB empty): starts at 00:00:00 in the configured timezone for the
//     calendar day lookbackHours (default 48) before now, matching daily aggregation boundaries.
//
// Calls are serialized per collector instance so overlapping invocations cannot clear each other's
// pendingReplayStart or race on lastCheckedAt. On success, lastCheckedAt updates to now even when
// zero new logs are returned.
func (c *SpendLogCollector) CollectIncremental(ctx context.Context, overlapWindow time.Duration, lookbackHours int) (*CollectResult, error) {
	if overlapWindow <= 0 {
		overlapWindow = defaultOverlapWindow
	}
	if lookbackHours <= 0 {
		lookbackHours = defaultLookbackHours
	}

	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	now := c.now().UTC()
	var startTime time.Time

	c.mu.RLock()
	checkpoint := c.lastCheckedAt
	replayStart := c.pendingReplayStart
	c.mu.RUnlock()

	if !checkpoint.IsZero() {
		if checkpoint.After(now) {
			// Wall clock stepped backward: query an overlap window ending at corrected now.
			startTime = now.Add(-overlapWindow)
		} else {
			// Online operation: bound query window using in-memory progress checkpoint.
			startTime = checkpoint.UTC().Add(-overlapWindow)
		}
	} else if !replayStart.IsZero() {
		// Incomplete startup/cold-start replay: keep the original lower bound so
		// partially committed newest-first pages cannot advance MAX(request_time)
		// past older uncommitted pages on retry.
		startTime = replayStart
	} else {
		latestTime, err := c.GetLatestRequestTime(ctx)
		if err != nil {
			return nil, err
		}
		if latestTime == nil {
			// Cold start: align to 00:00:00 in configured timezone for full calendar-day coverage.
			startTime = dayAlignedLookbackStart(now, lookbackHours, c.Location())
		} else {
			// Restart recovery: uncapped watermark replay from persisted MAX(request_time).
			startTime = latestTime.UTC().Add(-overlapWindow)
		}
	}

	if startTime.After(now) {
		startTime = now
	}

	if checkpoint.IsZero() && replayStart.IsZero() {
		c.mu.Lock()
		c.pendingReplayStart = startTime
		c.mu.Unlock()
	}

	res, err := c.CollectSpendLogs(ctx, startTime, now)
	if err != nil {
		return res, err
	}

	c.mu.Lock()
	c.lastCheckedAt = now
	c.pendingReplayStart = time.Time{}
	c.mu.Unlock()

	return res, nil
}
