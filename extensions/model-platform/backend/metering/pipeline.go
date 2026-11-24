package metering

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultPurgeBatchSize is the default number of raw spend log rows deleted per batch.
const DefaultPurgeBatchSize = 1000

// PurgeResult captures metrics produced by a raw spend log retention sweep.
type PurgeResult struct {
	CutoffTime   time.Time `json:"cutoff_time"`
	DeletedCount int64     `json:"deleted_count"`
	Batches      int       `json:"batches"`
	Skipped      bool      `json:"skipped"`
}

// PipelineResult captures metrics produced by a daily billing aggregation run.
type PipelineResult struct {
	StartTime         time.Time `json:"start_time"`
	EndTime           time.Time `json:"end_time"`
	AggregatedRecords int       `json:"aggregated_records"`
	TenantsImpacted   int       `json:"tenants_impacted"`
}

// UsagePipeline executes deterministic daily usage aggregation from raw spend logs
// into platform_tenant_usage_daily using replacement upsert semantics.
type UsagePipeline struct {
	db        *gorm.DB
	batchSize int
	loc       *time.Location
}

// PipelineOption configures UsagePipeline attributes.
type PipelineOption func(*UsagePipeline)

// WithPipelineBatchSize configures the batch upsert chunk size.
func WithPipelineBatchSize(size int) PipelineOption {
	return func(p *UsagePipeline) {
		if size > 0 {
			p.batchSize = size
		}
	}
}

// WithPipelineTimezone sets the business billing timezone for daily usage aggregation.
func WithPipelineTimezone(loc *time.Location) PipelineOption {
	return func(p *UsagePipeline) {
		if loc != nil {
			p.loc = loc
		}
	}
}

// NewUsagePipeline creates a new daily usage aggregation pipeline.
// Defaults timezone to METERING_TIMEZONE and batch size to METERING_PIPELINE_BATCH_SIZE environment variables.
func NewUsagePipeline(db *gorm.DB, opts ...PipelineOption) *UsagePipeline {
	batchSize := 100
	if v := os.Getenv("METERING_PIPELINE_BATCH_SIZE"); v != "" {
		if s, err := strconv.Atoi(v); err == nil && s > 0 {
			batchSize = s
		}
	}

	p := &UsagePipeline{
		db:        db,
		batchSize: batchSize,
		loc:       ResolveMeteringLocation(""),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Location returns the configured business billing timezone.
func (p *UsagePipeline) Location() *time.Location {
	if p.loc == nil {
		return time.UTC
	}
	return p.loc
}

// SetLocation updates the business billing timezone on the pipeline.
func (p *UsagePipeline) SetLocation(loc *time.Location) {
	if loc != nil {
		p.loc = loc
	}
}

// ResolveMeteringLocation parses a timezone string into a *time.Location.
// If tzStr is empty, it checks METERING_TIMEZONE environment variable.
// Falls back to time.UTC if empty or invalid.
func ResolveMeteringLocation(tzStr string) *time.Location {
	if strings.TrimSpace(tzStr) == "" {
		tzStr = os.Getenv("METERING_TIMEZONE")
	}
	tzStr = strings.TrimSpace(tzStr)
	if tzStr == "" || strings.EqualFold(tzStr, "UTC") {
		return time.UTC
	}
	if strings.EqualFold(tzStr, "Local") {
		return time.Local
	}

	// Try standard IANA name (e.g. "Asia/Shanghai", "America/New_York")
	if loc, err := time.LoadLocation(tzStr); err == nil {
		return loc
	}

	// Try numeric offset (e.g. "+08:00", "-05:00", "+0800", "+08")
	if loc, err := parseNumericOffset(tzStr); err == nil {
		return loc
	}

	return time.UTC
}

func parseNumericOffset(s string) (*time.Location, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "UTC")
	s = strings.TrimPrefix(s, "GMT")
	if len(s) == 0 {
		return nil, errors.New("empty offset")
	}

	sign := 1
	if s[0] == '+' {
		s = s[1:]
	} else if s[0] == '-' {
		sign = -1
		s = s[1:]
	} else {
		return nil, errors.New("missing sign in offset")
	}

	var hours, minutes int
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) != 2 {
			return nil, errors.New("invalid offset format")
		}
		var errH, errM error
		hours, errH = strconv.Atoi(parts[0])
		minutes, errM = strconv.Atoi(parts[1])
		if errH != nil || errM != nil {
			return nil, errors.New("malformed numeric offset")
		}
	} else if len(s) <= 2 {
		var err error
		hours, err = strconv.Atoi(s)
		if err != nil {
			return nil, errors.New("malformed numeric offset")
		}
	} else if len(s) == 4 {
		var errH, errM error
		hours, errH = strconv.Atoi(s[:2])
		minutes, errM = strconv.Atoi(s[2:])
		if errH != nil || errM != nil {
			return nil, errors.New("malformed numeric offset")
		}
	} else {
		return nil, errors.New("unsupported offset format")
	}

	if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
		return nil, errors.New("offset out of valid range (0-23 hours, 0-59 minutes)")
	}

	totalSeconds := sign * (hours*3600 + minutes*60)
	zoneName := fmt.Sprintf("UTC%+03d:%02d", sign*hours, minutes)
	return time.FixedZone(zoneName, totalSeconds), nil
}

// RunWindow executes daily billing aggregation for a rolling lookback window (default 48h).
// Start time is automatically day-aligned to 00:00:00 in the configured business timezone
// to prevent front-edge truncation of early-morning logs on the starting date during periodic runs.
func (p *UsagePipeline) RunWindow(ctx context.Context, lookbackHours int) (*PipelineResult, error) {
	if lookbackHours <= 0 {
		lookbackHours = 48
	}

	loc := p.Location()
	nowInLoc := time.Now().In(loc)
	rawStartInLoc := nowInLoc.Add(-time.Duration(lookbackHours) * time.Hour)
	alignedStartInLoc := time.Date(rawStartInLoc.Year(), rawStartInLoc.Month(), rawStartInLoc.Day(), 0, 0, 0, 0, loc)

	return p.AggregateDailyUsage(ctx, alignedStartInLoc, nowInLoc)
}

// AggregateDailyUsage aggregates raw logs from platform_gateway_spend_log between
// startTime and endTime, then performs deterministic replacement upsert into platform_tenant_usage_daily.
// This guarantees strict mathematical idempotency across repeated invocations and backfilling.
func (p *UsagePipeline) AggregateDailyUsage(ctx context.Context, startTime, endTime time.Time) (*PipelineResult, error) {
	if p.db == nil {
		return nil, errors.New("database handle cannot be nil")
	}
	if startTime.After(endTime) {
		return nil, errors.New("startTime cannot be after endTime")
	}

	// Day-align startTime in the configured business timezone to ensure full calendar-day coverage
	// and prevent partial replacement overwrite of earlier hours on the start day.
	loc := p.Location()
	stInLoc := startTime.In(loc)
	alignedStartInLoc := time.Date(stInLoc.Year(), stInLoc.Month(), stInLoc.Day(), 0, 0, 0, 0, loc)
	alignedStartTime := alignedStartInLoc.UTC()
	endUTC := endTime.UTC()

	dateExpr := resolveDateExpression(p.db, loc)

	var aggregated []struct {
		TenantID         string  `gorm:"column:tenant_id"`
		ModelName        string  `gorm:"column:model_name"`
		UsageDate        string  `gorm:"column:usage_date"`
		CallCount        int64   `gorm:"column:call_count"`
		PromptTokens     int64   `gorm:"column:prompt_tokens"`
		CompletionTokens int64   `gorm:"column:completion_tokens"`
		TotalTokens      int64   `gorm:"column:total_tokens"`
		CachedTokens     int64   `gorm:"column:cached_tokens"`
		CacheWriteTokens int64   `gorm:"column:cache_write_tokens"`
		ReasoningTokens  int64   `gorm:"column:reasoning_tokens"`
		TotalSpend       float64 `gorm:"column:total_spend"`
		PromptSpend      float64 `gorm:"column:prompt_spend"`
		CompletionSpend  float64 `gorm:"column:completion_spend"`
		CacheSavings     float64 `gorm:"column:cache_savings"`
	}

	selectClause := fmt.Sprintf(
		"tenant_id, model_name, %s AS usage_date, COUNT(*) AS call_count, "+
			"SUM(prompt_tokens) AS prompt_tokens, SUM(completion_tokens) AS completion_tokens, "+
			"SUM(total_tokens) AS total_tokens, SUM(cached_tokens) AS cached_tokens, "+
			"SUM(cache_write_tokens) AS cache_write_tokens, SUM(reasoning_tokens) AS reasoning_tokens, "+
			"SUM(spend) AS total_spend, SUM(prompt_spend) AS prompt_spend, "+
			"SUM(completion_spend) AS completion_spend, SUM(cache_savings) AS cache_savings",
		dateExpr,
	)
	groupClause := fmt.Sprintf("tenant_id, model_name, %s", dateExpr)

	err := p.db.WithContext(ctx).
		Table("platform_gateway_spend_log").
		Select(selectClause).
		Where("request_time >= ? AND request_time <= ? AND tenant_id != ''", alignedStartTime, endUTC).
		Group(groupClause).
		Scan(&aggregated).Error
	if err != nil {
		return nil, fmt.Errorf("failed to aggregate spend logs: %w", err)
	}

	result := &PipelineResult{
		StartTime:         alignedStartTime,
		EndTime:           endUTC,
		AggregatedRecords: len(aggregated),
	}

	if len(aggregated) == 0 {
		return result, nil
	}

	tenantMap := make(map[string]struct{})
	dailyRecords := make([]entity.TenantUsageDaily, 0, len(aggregated))
	now := time.Now().UTC()

	for _, item := range aggregated {
		tenantMap[item.TenantID] = struct{}{}
		dailyRecords = append(dailyRecords, entity.TenantUsageDaily{
			TenantID:         item.TenantID,
			ModelName:        item.ModelName,
			UsageDate:        item.UsageDate,
			CallCount:        item.CallCount,
			PromptTokens:     item.PromptTokens,
			CompletionTokens: item.CompletionTokens,
			TotalTokens:      item.TotalTokens,
			CachedTokens:     item.CachedTokens,
			CacheWriteTokens: item.CacheWriteTokens,
			ReasoningTokens:  item.ReasoningTokens,
			TotalSpend:       item.TotalSpend,
			PromptSpend:      item.PromptSpend,
			CompletionSpend:  item.CompletionSpend,
			CacheSavings:     item.CacheSavings,
			UpdatedAt:        now,
		})
	}
	result.TenantsImpacted = len(tenantMap)

	// Deterministic Replacement Upsert within a Database Transaction:
	// Overwrite values with newly aggregated ground-truth figures atomically.
	// Strictly NO additive increments (+=), preventing any double-counting on replay.
	batchSize := p.batchSize
	if batchSize <= 0 {
		batchSize = 100
	}

	err = p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := 0; i < len(dailyRecords); i += batchSize {
			end := i + batchSize
			if end > len(dailyRecords) {
				end = len(dailyRecords)
			}
			batch := dailyRecords[i:end]

			err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{
					{Name: "tenant_id"},
					{Name: "model_name"},
					{Name: "usage_date"},
				},
				DoUpdates: clause.AssignmentColumns([]string{
					"call_count",
					"prompt_tokens",
					"completion_tokens",
					"total_tokens",
					"cached_tokens",
					"cache_write_tokens",
					"reasoning_tokens",
					"total_spend",
					"prompt_spend",
					"completion_spend",
					"cache_savings",
					"updated_at",
				}),
			}).Create(&batch).Error
			if err != nil {
				return fmt.Errorf("failed to upsert daily usage batch: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// resolveDateExpression formats SQL date truncation appropriately across MySQL, PostgreSQL, and SQLite,
// honoring the target business billing timezone.
func resolveDateExpression(db *gorm.DB, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	now := time.Now().In(loc)
	_, offsetSec := now.Zone()

	sign := "+"
	absOffsetSec := offsetSec
	if absOffsetSec < 0 {
		sign = "-"
		absOffsetSec = -absOffsetSec
	}
	offsetStr := fmt.Sprintf("%s%02d:%02d", sign, absOffsetSec/3600, (absOffsetSec%3600)/60)

	if db == nil || db.Dialector == nil {
		return "DATE(request_time)"
	}
	switch strings.ToLower(db.Dialector.Name()) {
	case "sqlite":
		if offsetSec == 0 {
			return "strftime('%Y-%m-%d', request_time)"
		}
		return fmt.Sprintf("strftime('%%Y-%%m-%%d', datetime(request_time, '%s%d seconds'))", sign, absOffsetSec)

	case "postgres", "postgresql":
		locName := loc.String()
		if locName != "" && !strings.EqualFold(locName, "Local") && strings.Contains(locName, "/") {
			return fmt.Sprintf("to_char(request_time AT TIME ZONE '%s', 'YYYY-MM-DD')", locName)
		}
		if locName == "UTC" || offsetSec == 0 {
			return "to_char(request_time AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
		}
		return fmt.Sprintf("to_char(request_time AT TIME ZONE INTERVAL '%s', 'YYYY-MM-DD')", offsetStr)

	case "mysql":
		if offsetSec == 0 {
			return "DATE_FORMAT(request_time, '%Y-%m-%d')"
		}
		return fmt.Sprintf("DATE_FORMAT(CONVERT_TZ(request_time, '+00:00', '%s'), '%%Y-%%m-%%d')", offsetStr)

	default:
		return "DATE(request_time)"
	}
}

// QueryDailyUsage retrieves daily usage breakdown for a tenant within [startDate, endDate].
func QueryDailyUsage(ctx context.Context, db *gorm.DB, tenantID string, startDate, endDate string) ([]entity.TenantUsageDaily, error) {
	if db == nil {
		return nil, errors.New("database handle cannot be nil")
	}

	query := db.WithContext(ctx).Model(&entity.TenantUsageDaily{})
	if tenantID != "" {
		query = query.Where("tenant_id = ?", tenantID)
	}
	if startDate != "" {
		query = query.Where("usage_date >= ?", startDate)
	}
	if endDate != "" {
		query = query.Where("usage_date <= ?", endDate)
	}

	var results []entity.TenantUsageDaily
	err := query.Order("usage_date ASC, model_name ASC").Find(&results).Error
	if err != nil {
		return nil, fmt.Errorf("failed to query daily usage: %w", err)
	}

	return results, nil
}

// UsageSummary aggregates total consumption metrics over an arbitrary period.
type UsageSummary struct {
	TenantID              string           `json:"tenant_id"`
	TotalCalls            int64            `json:"total_calls"`
	TotalPromptTokens     int64            `json:"total_prompt_tokens"`
	TotalCompletionTokens int64            `json:"total_completion_tokens"`
	TotalTokens           int64            `json:"total_tokens"`
	TotalCachedTokens     int64            `json:"total_cached_tokens"`
	TotalCacheWriteTokens int64            `json:"total_cache_write_tokens"`
	TotalReasoningTokens  int64            `json:"total_reasoning_tokens"`
	TotalSpend            float64          `json:"total_spend"`
	TotalPromptSpend      float64          `json:"total_prompt_spend"`
	TotalCompletionSpend  float64          `json:"total_completion_spend"`
	TotalCacheSavings     float64          `json:"total_cache_savings"`
	ModelTokens           map[string]int64 `json:"model_tokens"`
	StartDate             string           `json:"start_date,omitempty"`
	EndDate               string           `json:"end_date,omitempty"`
}

// QueryUsageSummary calculates total usage and per-model token breakdown for a specific tenant.
// Requires non-empty tenantID to strictly prevent accidental cross-tenant data amalgamation.
func QueryUsageSummary(ctx context.Context, db *gorm.DB, tenantID string, startDate, endDate string) (*UsageSummary, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("tenantID cannot be empty for tenant usage summary")
	}

	return calculateUsageSummary(ctx, db, tenantID, startDate, endDate)
}

// QueryGlobalUsageSummary calculates total platform-wide usage across all tenants (for admin metrics).
func QueryGlobalUsageSummary(ctx context.Context, db *gorm.DB, startDate, endDate string) (*UsageSummary, error) {
	return calculateUsageSummary(ctx, db, "", startDate, endDate)
}

func calculateUsageSummary(ctx context.Context, db *gorm.DB, tenantID string, startDate, endDate string) (*UsageSummary, error) {
	dailyList, err := QueryDailyUsage(ctx, db, tenantID, startDate, endDate)
	if err != nil {
		return nil, err
	}

	summary := &UsageSummary{
		TenantID:    tenantID,
		ModelTokens: make(map[string]int64),
		StartDate:   startDate,
		EndDate:     endDate,
	}

	for _, d := range dailyList {
		summary.TotalCalls += d.CallCount
		summary.TotalPromptTokens += d.PromptTokens
		summary.TotalCompletionTokens += d.CompletionTokens
		summary.TotalTokens += d.TotalTokens
		summary.TotalCachedTokens += d.CachedTokens
		summary.TotalCacheWriteTokens += d.CacheWriteTokens
		summary.TotalReasoningTokens += d.ReasoningTokens
		summary.TotalSpend += d.TotalSpend
		summary.TotalPromptSpend += d.PromptSpend
		summary.TotalCompletionSpend += d.CompletionSpend
		summary.TotalCacheSavings += d.CacheSavings
		summary.ModelTokens[d.ModelName] += d.TotalTokens
	}

	return summary, nil
}

// PurgeExpiredSpendLogs deletes raw spend logs strictly older than cutoff (request_time < cutoff)
// from platform_gateway_spend_log in bounded primary-key batches to avoid long table locks and
// remain portable across MySQL, PostgreSQL, and SQLite.
// It never mutates or deletes rows from platform_tenant_usage_daily.
func (p *UsagePipeline) PurgeExpiredSpendLogs(ctx context.Context, cutoff time.Time, batchSize int) (*PurgeResult, error) {
	if p.db == nil {
		return nil, errors.New("database handle cannot be nil")
	}
	if cutoff.IsZero() {
		return nil, errors.New("cutoff time cannot be zero")
	}
	if batchSize <= 0 {
		batchSize = DefaultPurgeBatchSize
	}

	cutoffUTC := cutoff.UTC()
	result := &PurgeResult{
		CutoffTime: cutoffUTC,
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		var ids []string
		err := p.db.WithContext(ctx).
			Model(&entity.GatewaySpendLog{}).
			Where("request_time < ?", cutoffUTC).
			Order("request_time ASC, spend_id ASC").
			Limit(batchSize).
			Pluck("spend_id", &ids).Error
		if err != nil {
			return result, fmt.Errorf("failed to select expired spend logs: %w", err)
		}

		if len(ids) == 0 {
			break
		}

		res := p.db.WithContext(ctx).
			Where("spend_id IN ?", ids).
			Delete(&entity.GatewaySpendLog{})
		if res.Error != nil {
			return result, fmt.Errorf("failed to delete expired spend logs batch: %w", res.Error)
		}

		result.Batches++
		result.DeletedCount += res.RowsAffected

		if len(ids) < batchSize {
			break
		}
	}

	return result, nil
}
