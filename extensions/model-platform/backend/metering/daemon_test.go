package metering

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// timeFilteredSpendLogClient simulates LiteLLM /spend/logs/v2 filtering by [StartDate, EndDate]
// with pagination and records every query issued against it.
type timeFilteredSpendLogClient struct {
	mu      sync.Mutex
	entries []gateway.SpendLogEntry
	reqs    []gateway.SpendLogQueryParams
	err     error
}

func (m *timeFilteredSpendLogClient) GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, params)
	if m.err != nil {
		return nil, m.err
	}

	startTime, err := time.Parse("2006-01-02 15:04:05", params.StartDate)
	if err != nil {
		return nil, err
	}
	endTime, err := time.Parse("2006-01-02 15:04:05", params.EndDate)
	if err != nil {
		return nil, err
	}

	var matched []gateway.SpendLogEntry
	for _, e := range m.entries {
		reqTime := ParseRequestTime(e.StartTime, e.EndTime)
		if !reqTime.Before(startTime) && !reqTime.After(endTime) {
			matched = append(matched, e)
		}
	}

	if params.Page > 0 && params.PageSize > 0 {
		start := (params.Page - 1) * params.PageSize
		if start >= len(matched) {
			return []gateway.SpendLogEntry{}, nil
		}
		end := start + params.PageSize
		if end > len(matched) {
			end = len(matched)
		}
		return matched[start:end], nil
	}
	return matched, nil
}

func setupDaemonTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:daemon_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	if err := migration.MigrateSpendLogSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate spend log schema: %v", err)
	}
	if err := migration.MigrateUsageSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate usage schema: %v", err)
	}

	return db
}

func TestMeteringDaemon_ColdStartAndIncremental(t *testing.T) {
	db := setupDaemonTestDB(t)

	mockClient := &mockSpendLogClient{
		entries: []gateway.SpendLogEntry{
			{
				SpendID:          "entry-1",
				TeamID:           "tenant-alpha",
				Model:            "gpt-4o",
				TotalTokens:      1000,
				PromptTokens:     800,
				CompletionTokens: 200,
				Spend:            0.05,
				StartTime:        time.Now().UTC().Add(-10 * time.Minute).Format("2006-01-02 15:04:05"),
			},
		},
	}

	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)
	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(100*time.Millisecond),
		WithAggregationInterval(1*time.Hour),
		WithOverlapWindow(5*time.Minute),
		WithLookbackHours(72),
	)

	// 1. Initial Ingestion (Cold Start Poll)
	cRes1, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce failed during cold start: %v", err)
	}
	if cRes1 == nil || cRes1.InsertedCount != 1 {
		t.Fatalf("expected 1 inserted entry on cold start, got %+v", cRes1)
	}

	// 2. Initial Aggregation
	pRes1, err := daemon.AggregateOnce(context.Background())
	if err != nil {
		t.Fatalf("AggregateOnce failed: %v", err)
	}
	if pRes1 == nil || pRes1.AggregatedRecords == 0 {
		t.Fatalf("expected pipeline aggregation to run on new records, got %+v", pRes1)
	}

	// Verify daily usage record exists in database
	var usageCount int64
	db.Model(&entity.TenantUsageDaily{}).Where("tenant_id = ?", "tenant-alpha").Count(&usageCount)
	if usageCount == 0 {
		t.Fatalf("expected TenantUsageDaily record created in DB")
	}

	// 3. Second Ingestion (Watermark Incremental)
	mockClient.entries = []gateway.SpendLogEntry{
		{
			SpendID:          "entry-2",
			TeamID:           "tenant-alpha",
			Model:            "gpt-4o",
			TotalTokens:      500,
			PromptTokens:     400,
			CompletionTokens: 100,
			Spend:            0.02,
			StartTime:        time.Now().UTC().Format("2006-01-02 15:04:05"),
		},
	}

	cRes2, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce failed during incremental cycle: %v", err)
	}
	if cRes2 == nil || cRes2.InsertedCount != 1 {
		t.Fatalf("expected 1 inserted entry in incremental cycle, got %+v", cRes2)
	}
	if daemon.SuccessfulPolls() != 2 {
		t.Fatalf("expected 2 successful polls, got %d", daemon.SuccessfulPolls())
	}
}

func TestMeteringDaemon_GracefulShutdown(t *testing.T) {
	db := setupDaemonTestDB(t)
	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(20*time.Millisecond),
		WithAggregationInterval(30*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon.Start(ctx)
	// Calling Start again should be a safe no-op and not spawn duplicate workers
	daemon.Start(ctx)
	// Allow it to run at least one tick
	time.Sleep(60 * time.Millisecond)

	// Stop daemon
	daemon.Stop()

	// Subsequent Stop and Start calls should be safe (idempotent / no-op)
	daemon.Stop()
	daemon.Start(ctx)

	if daemon.TotalPolls() < 1 {
		t.Fatalf("expected at least 1 poll before stop, got %d", daemon.TotalPolls())
	}
	if daemon.TotalAggregations() < 1 {
		t.Fatalf("expected at least 1 aggregation before stop, got %d", daemon.TotalAggregations())
	}
}

func TestMeteringDaemon_Disabled(t *testing.T) {
	db := setupDaemonTestDB(t)
	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	daemon := NewMeteringDaemon(collector, pipeline,
		WithDaemonEnabled(false),
		WithPollInterval(20*time.Millisecond),
		WithAggregationInterval(20*time.Millisecond),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	daemon.Stop()

	if daemon.TotalPolls() != 0 || daemon.TotalAggregations() != 0 {
		t.Fatalf("disabled daemon should not execute any polls or aggregations, polls=%d, aggs=%d",
			daemon.TotalPolls(), daemon.TotalAggregations())
	}
}

func TestMeteringDaemon_ErrorHandlingAndMetrics(t *testing.T) {
	db := setupDaemonTestDB(t)
	mockClient := &mockSpendLogClient{
		err: errors.New("simulated gateway connection failure"),
	}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	daemon := NewMeteringDaemon(collector, pipeline)

	// Test poll error
	_, err := daemon.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("expected error from simulated gateway failure, got nil")
	}

	if daemon.FailedPolls() != 1 {
		t.Fatalf("expected 1 failed poll, got %d", daemon.FailedPolls())
	}
	if daemon.SuccessfulPolls() != 0 {
		t.Fatalf("expected 0 successful polls, got %d", daemon.SuccessfulPolls())
	}
	if daemon.LastPollError() == nil || !strings.Contains(daemon.LastPollError().Error(), "simulated gateway connection failure") {
		t.Fatalf("unexpected last poll error: %v", daemon.LastPollError())
	}

	// Test nil pipeline aggregation error
	nilPipelineDaemon := NewMeteringDaemon(collector, nil)
	_, err = nilPipelineDaemon.AggregateOnce(context.Background())
	if err == nil {
		t.Fatalf("expected error from nil pipeline, got nil")
	}
	if nilPipelineDaemon.FailedAggregations() != 1 {
		t.Fatalf("expected 1 failed aggregation, got %d", nilPipelineDaemon.FailedAggregations())
	}

	metrics := daemon.GetMetrics()
	if metrics.TotalPolls != 1 || metrics.FailedPolls != 1 || metrics.LastPollError == "" {
		t.Fatalf("unexpected metrics snapshot: %+v", metrics)
	}
}

func TestMeteringDaemon_SpendLogRetention_DisabledByDefault(t *testing.T) {
	t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", "")
	db := setupDaemonTestDB(t)
	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	oldLog := entity.GatewaySpendLog{
		SpendID:     "ancient-default-1",
		TenantID:    "tenant-retention",
		ModelName:   "gpt-4o",
		TotalTokens: 100,
		Spend:       0.01,
		RequestTime: time.Now().UTC().Add(-365 * 24 * time.Hour),
	}
	if err := db.Create(&oldLog).Error; err != nil {
		t.Fatalf("failed to seed old spend log: %v", err)
	}

	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(20*time.Millisecond),
		WithAggregationInterval(20*time.Millisecond),
	)
	daemon.purgeInterval = 20 * time.Millisecond

	res, err := daemon.PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce returned unexpected error when disabled by default: %v", err)
	}
	if !res.Skipped || res.DeletedCount != 0 {
		t.Fatalf("expected skipped purge by default, got %+v", res)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	daemon.Stop()

	if daemon.TotalPurges() != 0 {
		t.Fatalf("expected purge worker not to run by default, got totalPurges=%d", daemon.TotalPurges())
	}

	var count int64
	db.Model(&entity.GatewaySpendLog{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected ancient log to be retained when retention is disabled by default, got count=%d", count)
	}
}

func TestMeteringDaemon_SpendLogRetention_90DaysAndLedgerInvariant(t *testing.T) {
	t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", "90")
	db := setupDaemonTestDB(t)
	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	now := time.Now().UTC()
	oldTime := now.Add(-95 * 24 * time.Hour)
	borderExpiredTime := now.Add(-90*24*time.Hour - time.Hour)
	recentTime := now.Add(-89 * 24 * time.Hour)
	freshTime := now.Add(-2 * time.Hour)

	seedLogs := []entity.GatewaySpendLog{
		{
			SpendID:     "expired-95d",
			TenantID:    "tenant-ret",
			ModelName:   "gpt-4o",
			TotalTokens: 1000,
			Spend:       0.10,
			RequestTime: oldTime,
		},
		{
			SpendID:     "expired-90d-plus-1h",
			TenantID:    "tenant-ret",
			ModelName:   "gpt-4o",
			TotalTokens: 500,
			Spend:       0.05,
			RequestTime: borderExpiredTime,
		},
		{
			SpendID:     "retained-89d",
			TenantID:    "tenant-ret",
			ModelName:   "gpt-4o",
			TotalTokens: 300,
			Spend:       0.03,
			RequestTime: recentTime,
		},
		{
			SpendID:     "retained-2h",
			TenantID:    "tenant-ret",
			ModelName:   "gpt-4o",
			TotalTokens: 200,
			Spend:       0.02,
			RequestTime: freshTime,
		},
	}
	for _, l := range seedLogs {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("failed to seed log %s: %v", l.SpendID, err)
		}
	}

	// Aggregate all seeded days into platform_tenant_usage_daily before purging.
	if _, err := pipeline.AggregateDailyUsage(context.Background(), oldTime.Add(-24*time.Hour), now); err != nil {
		t.Fatalf("failed to pre-aggregate daily usage: %v", err)
	}

	oldDate := oldTime.Format("2006-01-02")
	var oldDailyBefore entity.TenantUsageDaily
	if err := db.Where("tenant_id = ? AND usage_date = ?", "tenant-ret", oldDate).First(&oldDailyBefore).Error; err != nil {
		t.Fatalf("expected daily usage row for %s before purge: %v", oldDate, err)
	}

	daemon := NewMeteringDaemon(collector, pipeline)

	res, err := daemon.PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce failed: %v", err)
	}
	if res.Skipped || res.DeletedCount != 2 {
		t.Fatalf("expected 2 expired logs deleted, got %+v", res)
	}

	// Verify only logs within the 90-day window remain.
	var remaining []entity.GatewaySpendLog
	if err := db.Order("spend_id ASC").Find(&remaining).Error; err != nil {
		t.Fatalf("failed to query remaining logs: %v", err)
	}
	if len(remaining) != 2 || remaining[0].SpendID != "retained-2h" || remaining[1].SpendID != "retained-89d" {
		t.Fatalf("unexpected remaining spend logs: %+v", remaining)
	}

	// Verify platform_tenant_usage_daily is untouched and historical financial truth remains queryable.
	var oldDailyAfter entity.TenantUsageDaily
	if err := db.Where("tenant_id = ? AND usage_date = ?", "tenant-ret", oldDate).First(&oldDailyAfter).Error; err != nil {
		t.Fatalf("historical daily usage row for %s disappeared after purge: %v", oldDate, err)
	}
	if oldDailyAfter.CallCount != oldDailyBefore.CallCount ||
		oldDailyAfter.TotalTokens != oldDailyBefore.TotalTokens ||
		oldDailyAfter.TotalSpend != oldDailyBefore.TotalSpend {
		t.Fatalf("historical daily usage row mutated: before=%+v after=%+v", oldDailyBefore, oldDailyAfter)
	}

	summary, err := QueryUsageSummary(context.Background(), db, "tenant-ret", oldDate, now.Format("2006-01-02"))
	if err != nil {
		t.Fatalf("QueryUsageSummary failed after purge: %v", err)
	}
	if summary.TotalTokens != 2000 || summary.TotalCalls != 4 {
		t.Fatalf("expected full historical summary preserved (2000 tokens, 4 calls), got %+v", summary)
	}

	// Verify purge metrics snapshot.
	metrics := daemon.GetMetrics()
	if metrics.TotalPurges != 1 || metrics.SuccessfulPurges != 1 || metrics.FailedPurges != 0 || metrics.TotalPurgedRows != 2 {
		t.Fatalf("unexpected purge metrics: %+v", metrics)
	}
	if metrics.LastPurgeTime.IsZero() || metrics.LastPurgeError != "" {
		t.Fatalf("unexpected purge timestamps/error: %+v", metrics)
	}
}

func TestMeteringDaemon_SpendLogRetention_ZeroOrNegativeDisablesPurge(t *testing.T) {
	for _, val := range []string{"0", "-7"} {
		t.Run("env_"+val, func(t *testing.T) {
			t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", val)
			db := setupDaemonTestDB(t)
			collector := NewSpendLogCollector(db, &mockSpendLogClient{})
			pipeline := NewUsagePipeline(db)

			if err := db.Create(&entity.GatewaySpendLog{
				SpendID:     "keep-forever",
				TenantID:    "tenant-zero",
				ModelName:   "gpt-4o",
				TotalTokens: 100,
				RequestTime: time.Now().UTC().Add(-200 * 24 * time.Hour),
			}).Error; err != nil {
				t.Fatalf("failed to seed spend log: %v", err)
			}

			daemon := NewMeteringDaemon(collector, pipeline)
			res, err := daemon.PurgeOnce(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !res.Skipped || res.DeletedCount != 0 {
				t.Fatalf("expected skipped result for retention=%s, got %+v", val, res)
			}

			var count int64
			db.Model(&entity.GatewaySpendLog{}).Count(&count)
			if count != 1 {
				t.Fatalf("expected 1 retained log when retention=%s, got %d", val, count)
			}
		})
	}
}

func TestMeteringDaemon_SpendLogRetention_ClampsToAggregationLookbackWindow(t *testing.T) {
	t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", "")
	db := setupDaemonTestDB(t)
	collector := NewSpendLogCollector(db, &mockSpendLogClient{})
	pipeline := NewUsagePipeline(db)

	now := time.Now().UTC()
	// Configure aggressive 1-day retention alongside a 72h (3-day) aggregation lookback window.
	// The 2-day-old log is still inside the rolling aggregation window and must NOT be purged,
	// while the 5-day-old log is safely outside and should be purged.
	logs := []entity.GatewaySpendLog{
		{
			SpendID:     "safe-to-purge-5d",
			TenantID:    "tenant-clamp",
			ModelName:   "gpt-4o",
			TotalTokens: 100,
			RequestTime: now.Add(-5 * 24 * time.Hour),
		},
		{
			SpendID:     "protected-in-window-2d",
			TenantID:    "tenant-clamp",
			ModelName:   "gpt-4o",
			TotalTokens: 200,
			RequestTime: now.Add(-48 * time.Hour),
		},
	}
	for _, l := range logs {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("failed to seed log %s: %v", l.SpendID, err)
		}
	}

	daemon := NewMeteringDaemon(collector, pipeline,
		WithLookbackHours(72),
		WithRetentionDays(1),
	)

	res, err := daemon.PurgeOnce(context.Background())
	if err != nil {
		t.Fatalf("PurgeOnce failed: %v", err)
	}
	if res.DeletedCount != 1 {
		t.Fatalf("expected only 1 log outside aggregation window to be purged, got %+v", res)
	}

	var remaining []entity.GatewaySpendLog
	db.Find(&remaining)
	if len(remaining) != 1 || remaining[0].SpendID != "protected-in-window-2d" {
		t.Fatalf("expected log inside aggregation window to be protected, got %+v", remaining)
	}
}

func TestMeteringDaemon_SpendLogRetention_BackgroundWorkerAndErrorHandling(t *testing.T) {
	t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", "")
	db := setupDaemonTestDB(t)
	collector := NewSpendLogCollector(db, &mockSpendLogClient{})
	pipeline := NewUsagePipeline(db)

	if err := db.Create(&entity.GatewaySpendLog{
		SpendID:     "worker-expired",
		TenantID:    "tenant-worker",
		ModelName:   "gpt-4o",
		RequestTime: time.Now().UTC().Add(-100 * 24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("failed to seed log: %v", err)
	}

	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(50*time.Millisecond),
		WithAggregationInterval(50*time.Millisecond),
		WithRetentionDays(90),
	)
	daemon.purgeInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	daemon.Start(ctx)
	time.Sleep(60 * time.Millisecond)
	daemon.Stop()

	if daemon.TotalPurges() < 1 || daemon.SuccessfulPurges() < 1 || daemon.TotalPurgedRows() != 1 {
		t.Fatalf("expected background purge worker to execute and delete 1 row, metrics=%+v", daemon.GetMetrics())
	}

	var count int64
	db.Model(&entity.GatewaySpendLog{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected expired log deleted by background worker, got count=%d", count)
	}

	// Nil pipeline returns error and updates failure metrics when retention is enabled.
	nilPipelineDaemon := NewMeteringDaemon(collector, nil, WithRetentionDays(90))
	if _, err := nilPipelineDaemon.PurgeOnce(context.Background()); err == nil {
		t.Fatal("expected error for nil pipeline on PurgeOnce, got nil")
	}
	if nilPipelineDaemon.FailedPurges() != 1 || nilPipelineDaemon.LastPurgeError() == nil {
		t.Fatalf("expected failed purge metrics recorded, got %+v", nilPipelineDaemon.GetMetrics())
	}
}

func TestMeteringDaemon_SpendLogRetention_PartialFailureCountsDeletedRows(t *testing.T) {
	t.Setenv("METERING_SPEND_LOG_RETENTION_DAYS", "")
	db := setupDaemonTestDB(t)
	collector := NewSpendLogCollector(db, &mockSpendLogClient{})
	pipeline := NewUsagePipeline(db)

	oldTime := time.Now().UTC().Add(-100 * 24 * time.Hour)
	totalRows := DefaultPurgeBatchSize + 5
	batch := make([]entity.GatewaySpendLog, 0, totalRows)
	for i := 0; i < totalRows; i++ {
		batch = append(batch, entity.GatewaySpendLog{
			SpendID:     fmt.Sprintf("partial-fail-%04d", i),
			TenantID:    "tenant-partial",
			ModelName:   "gpt-4o",
			RequestTime: oldTime.Add(time.Duration(i) * time.Second),
		})
	}
	if err := db.CreateInBatches(batch, 200).Error; err != nil {
		t.Fatalf("failed to seed expired logs: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel context right after the first delete batch commits so batch 2 aborts.
	if err := db.Callback().Delete().After("gorm:delete").Register("test:cancel_after_first_delete", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == (entity.GatewaySpendLog{}).TableName() {
			cancel()
		}
	}); err != nil {
		t.Fatalf("failed to register delete callback: %v", err)
	}

	daemon := NewMeteringDaemon(collector, pipeline, WithRetentionDays(90))
	res, err := daemon.PurgeOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled after first batch, got err=%v", err)
	}
	if res == nil || res.DeletedCount != DefaultPurgeBatchSize {
		t.Fatalf("expected %d deleted rows before cancellation, got %+v", DefaultPurgeBatchSize, res)
	}

	metrics := daemon.GetMetrics()
	if metrics.TotalPurges != 1 || metrics.FailedPurges != 1 || metrics.SuccessfulPurges != 0 {
		t.Fatalf("unexpected purge attempt counters: %+v", metrics)
	}
	if metrics.TotalPurgedRows != DefaultPurgeBatchSize {
		t.Fatalf("expected TotalPurgedRows=%d on partial failure, got %d", DefaultPurgeBatchSize, metrics.TotalPurgedRows)
	}
}

func TestMeteringDaemon_EnvLookbackHoursAndTimezoneSync(t *testing.T) {
	t.Setenv("METERING_LOOKBACK_HOURS", "72")
	t.Setenv("METERING_TIMEZONE", "UTC")

	db := setupDaemonTestDB(t)
	mockClient := &timeFilteredSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)
	pipeline := NewUsagePipeline(db)

	cst := time.FixedZone("UTC+08:00", 8*3600)
	fixedNow := time.Date(2026, 11, 24, 3, 15, 0, 0, cst)
	collector.nowFunc = func() time.Time { return fixedNow }

	daemon := NewMeteringDaemon(collector, pipeline, WithDaemonTimezone(cst))
	daemon.nowFunc = func() time.Time { return fixedNow }

	if daemon.lookbackHours != 72 {
		t.Fatalf("expected METERING_LOOKBACK_HOURS=72, got %d", daemon.lookbackHours)
	}
	if collector.Location() != cst || pipeline.Location() != cst {
		t.Fatalf("expected WithDaemonTimezone to synchronize both collector and pipeline")
	}

	// Cold start with 72h lookback in UTC+08:00:
	// 2026-11-24 03:15 +08:00 - 72h = 2026-11-21 03:15 +08:00 -> day-aligned 2026-11-21 00:00 +08:00
	// == 2026-11-20 16:00:00 UTC.
	cRes, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("cold-start PollOnce failed: %v", err)
	}
	expectedStart := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	if !cRes.StartTime.Equal(expectedStart) {
		t.Fatalf("expected day-aligned cold start %v, got %v", expectedStart, cRes.StartTime)
	}

	// Explicit option overrides environment variable.
	overrideDaemon := NewMeteringDaemon(collector, pipeline, WithLookbackHours(24))
	if overrideDaemon.lookbackHours != 24 {
		t.Fatalf("expected WithLookbackHours(24) to override env, got %d", overrideDaemon.lookbackHours)
	}
}

func TestMeteringDaemon_IdleWindowInvariant(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupDaemonTestDB(t)
	mockClient := &timeFilteredSpendLogClient{} // returns 0 records on every poll

	currentTime := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	// Seed an old record 3 days ago to verify MAX(request_time) does not dilate online polls.
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "old-idle-record",
		TenantID:    "tenant-idle",
		ModelName:   "gpt-4o",
		RequestTime: currentTime.Add(-72 * time.Hour),
	})

	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }
	pipeline := NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(5*time.Minute),
		WithOverlapWindow(5*time.Minute),
	)
	daemon.nowFunc = func() time.Time { return currentTime }

	// First poll establishes in-memory checkpoint after restart.
	if _, err := daemon.PollOnce(context.Background()); err != nil {
		t.Fatalf("initial PollOnce failed: %v", err)
	}

	// Advance 5 minutes per cycle across multiple idle hours; each poll must start at
	// previous checkpoint minus the configured 5-minute overlap and end at currentTime.
	for i := 0; i < 12; i++ {
		prevTime := currentTime
		currentTime = currentTime.Add(5 * time.Minute)
		res, err := daemon.PollOnce(context.Background())
		if err != nil {
			t.Fatalf("idle poll %d failed: %v", i+1, err)
		}
		if res.InsertedCount != 0 {
			t.Fatalf("idle poll %d: expected 0 inserted, got %d", i+1, res.InsertedCount)
		}
		expectedStart := prevTime.Add(-5 * time.Minute)
		if !res.StartTime.Equal(expectedStart) || !res.EndTime.Equal(currentTime) {
			t.Fatalf("idle poll %d: expected [%v, %v], got [%v, %v]",
				i+1, expectedStart, currentTime, res.StartTime, res.EndTime)
		}
	}
}

func TestMeteringDaemon_MultiDayOutageRecoveryAndCatchup(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupDaemonTestDB(t)

	// Outage started right after a baseline record 5 days ago; system restarts at 2026-11-24 14:00 UTC.
	restartTime := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	seedTime := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	tenantID := "tenant-outage"

	if err := db.Create(&entity.GatewaySpendLog{
		SpendID:          "pre-outage-seed",
		TenantID:         tenantID,
		ModelName:        "gpt-4o",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Spend:            0.01,
		RequestTime:      seedTime,
	}).Error; err != nil {
		t.Fatalf("failed to seed pre-outage log: %v", err)
	}

	// Mock gateway contains spend logs spanning all 5 calendar days during downtime (Nov 20..24)
	// plus an additional log on Nov 19 after the seed timestamp.
	outageDays := []struct {
		id     string
		ts     time.Time
		tokens int
		spend  float64
	}{
		{"day1-log", time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC), 1000, 0.10},
		{"day2-log", time.Date(2026, 11, 21, 11, 15, 0, 0, time.UTC), 2000, 0.20},
		{"day3-log", time.Date(2026, 11, 22, 16, 45, 0, 0, time.UTC), 3000, 0.30},
		{"day4-log", time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC), 4000, 0.40},
		{"day5-log", time.Date(2026, 11, 24, 13, 30, 0, 0, time.UTC), 5000, 0.50},
	}

	mockClient := &timeFilteredSpendLogClient{}
	for _, d := range outageDays {
		mockClient.entries = append(mockClient.entries, gateway.SpendLogEntry{
			SpendID:          d.id,
			TeamID:           tenantID,
			Model:            "gpt-4o",
			PromptTokens:     d.tokens / 2,
			CompletionTokens: d.tokens / 2,
			TotalTokens:      d.tokens,
			Spend:            d.spend,
			StartTime:        d.ts.Format("2006-01-02 15:04:05"),
		})
	}

	// Fresh collector + daemon simulate a process restart (lastCheckedAt is zero).
	currentTime := restartTime
	collector := NewSpendLogCollector(db, mockClient, WithBatchSize(2), WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }
	pipeline := NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	daemon := NewMeteringDaemon(collector, pipeline,
		WithPollInterval(5*time.Minute),
		WithOverlapWindow(5*time.Minute),
		WithLookbackHours(48),
	)
	daemon.nowFunc = func() time.Time { return currentTime }

	// Single PollOnce must recover all 5 days without truncation AND automatically
	// trigger dynamic multi-day catch-up aggregation without manually invoking AggregateOnce.
	cRes, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("recovery PollOnce failed: %v", err)
	}
	expectedStart := seedTime.Add(-5 * time.Minute)
	if !cRes.StartTime.Equal(expectedStart) {
		t.Fatalf("expected uncapped recovery start %v, got %v", expectedStart, cRes.StartTime)
	}
	if cRes.InsertedCount != 5 {
		t.Fatalf("expected 5 recovered spend logs across 5 outage days, got %d", cRes.InsertedCount)
	}
	if daemon.SuccessfulAggregations() != 1 {
		t.Fatalf("expected dynamic multi-day catch-up aggregation to run once, got %d", daemon.SuccessfulAggregations())
	}

	// Verify all 5 outage dates (plus the seeded day) exist in platform_tenant_usage_daily with exact totals.
	dailyRows, err := QueryDailyUsage(context.Background(), db, tenantID, "2026-11-19", "2026-11-24")
	if err != nil {
		t.Fatalf("QueryDailyUsage failed: %v", err)
	}
	byDate := make(map[string]entity.TenantUsageDaily, len(dailyRows))
	for _, row := range dailyRows {
		byDate[row.UsageDate] = row
	}
	for _, d := range outageDays {
		dateKey := d.ts.Format("2006-01-02")
		row, ok := byDate[dateKey]
		if !ok {
			t.Fatalf("missing daily usage record for recovered date %s", dateKey)
		}
		if row.TotalTokens != int64(d.tokens) || math.Abs(row.TotalSpend-d.spend) > 1e-6 {
			t.Fatalf("unexpected daily usage on %s: tokens=%d (want %d) spend=%.6f (want %.6f)",
				dateKey, row.TotalTokens, d.tokens, row.TotalSpend, d.spend)
		}
	}

	// Next poll 5 minutes later must settle immediately into the routine ~10-minute window
	// and must not trigger another catch-up aggregation.
	currentTime = currentTime.Add(5 * time.Minute)
	cRes2, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("post-recovery PollOnce failed: %v", err)
	}
	if span := cRes2.EndTime.Sub(cRes2.StartTime); span != 10*time.Minute {
		t.Fatalf("expected daemon to settle into 10m routine window, got %v", span)
	}
	if daemon.SuccessfulAggregations() != 1 {
		t.Fatalf("expected no extra catch-up aggregation during routine poll, got %d", daemon.SuccessfulAggregations())
	}
}

func TestMeteringDaemon_CatchupSelfHealsAfterAggregationFailure(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupDaemonTestDB(t)

	restartTime := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	seedTime := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	tenantID := "tenant-retry"

	db.Create(&entity.GatewaySpendLog{
		SpendID:     "retry-seed",
		TenantID:    tenantID,
		ModelName:   "gpt-4o",
		TotalTokens: 100,
		Spend:       0.01,
		RequestTime: seedTime,
	})

	mockClient := &timeFilteredSpendLogClient{
		entries: []gateway.SpendLogEntry{
			{
				SpendID:          "retry-day-old",
				TeamID:           tenantID,
				Model:            "gpt-4o",
				PromptTokens:     600,
				CompletionTokens: 400,
				TotalTokens:      1000,
				Spend:            0.12,
				StartTime:        time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
			},
		},
	}

	currentTime := restartTime
	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }

	// Broken pipeline (nil DB) forces the initial catch-up aggregation to fail while
	// raw spend log ingestion succeeds and advances the in-memory checkpoint.
	brokenPipeline := NewUsagePipeline(nil, WithPipelineTimezone(time.UTC))
	daemon := NewMeteringDaemon(collector, brokenPipeline,
		WithOverlapWindow(5*time.Minute),
		WithLookbackHours(48),
	)
	daemon.nowFunc = func() time.Time { return currentTime }

	cRes1, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce should return ingested result even if catch-up aggregation fails: %v", err)
	}
	if cRes1.InsertedCount != 1 {
		t.Fatalf("expected 1 inserted log, got %d", cRes1.InsertedCount)
	}
	if daemon.FailedAggregations() != 1 {
		t.Fatalf("expected 1 failed catch-up aggregation, got %d", daemon.FailedAggregations())
	}

	// Restore pipeline handle; next routine poll has 0 new logs and a 10m gateway window,
	// yet must still replay and heal the pending historical catch-up range.
	daemon.pipeline = NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	currentTime = currentTime.Add(5 * time.Minute)

	cRes2, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("healing PollOnce failed: %v", err)
	}
	if cRes2.InsertedCount != 0 {
		t.Fatalf("expected 0 new logs on healing poll, got %d", cRes2.InsertedCount)
	}
	if span := cRes2.EndTime.Sub(cRes2.StartTime); span != 10*time.Minute {
		t.Fatalf("expected gateway query to stay bounded at 10m without re-pulling 5 days, got %v", span)
	}
	if daemon.SuccessfulAggregations() != 1 {
		t.Fatalf("expected pending catch-up aggregation to succeed on retry, got %d", daemon.SuccessfulAggregations())
	}

	dailyRows, err := QueryDailyUsage(context.Background(), db, tenantID, "2026-11-20", "2026-11-20")
	if err != nil {
		t.Fatalf("QueryDailyUsage failed: %v", err)
	}
	if len(dailyRows) != 1 || dailyRows[0].TotalTokens != 1000 {
		t.Fatalf("expected 2026-11-20 daily usage healed with 1000 tokens, got %+v", dailyRows)
	}
}

type failOnPageClient struct {
	mu       sync.Mutex
	entries  []gateway.SpendLogEntry
	failPage int
}

func (f *failOnPageClient) GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPage > 0 && params.Page == f.failPage {
		return nil, errors.New("simulated transient page failure")
	}
	start := (params.Page - 1) * params.PageSize
	if start >= len(f.entries) {
		return []gateway.SpendLogEntry{}, nil
	}
	end := start + params.PageSize
	if end > len(f.entries) {
		end = len(f.entries)
	}
	return f.entries[start:end], nil
}

func TestMeteringDaemon_PartialPageFailureRetainsCatchupWork(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupDaemonTestDB(t)

	restartTime := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	seedTime := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	tenantID := "tenant-partial-page"

	db.Create(&entity.GatewaySpendLog{
		SpendID:     "partial-seed",
		TenantID:    tenantID,
		ModelName:   "gpt-4o",
		TotalTokens: 100,
		Spend:       0.01,
		RequestTime: seedTime,
	})

	// Two entries with batchSize=2: page 1 inserts both Nov 20 records, page 2 fails transiently.
	mockClient := &failOnPageClient{
		failPage: 2,
		entries: []gateway.SpendLogEntry{
			{
				SpendID:          "partial-p1-a",
				TeamID:           tenantID,
				Model:            "gpt-4o",
				PromptTokens:     300,
				CompletionTokens: 200,
				TotalTokens:      500,
				Spend:            0.05,
				StartTime:        time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
			},
			{
				SpendID:          "partial-p1-b",
				TeamID:           tenantID,
				Model:            "gpt-4o",
				PromptTokens:     400,
				CompletionTokens: 300,
				TotalTokens:      700,
				Spend:            0.07,
				StartTime:        time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
			},
		},
	}

	currentTime := restartTime
	collector := NewSpendLogCollector(db, mockClient, WithBatchSize(2), WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }
	pipeline := NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	daemon := NewMeteringDaemon(collector, pipeline,
		WithOverlapWindow(5*time.Minute),
		WithLookbackHours(48),
	)
	daemon.nowFunc = func() time.Time { return currentTime }

	// Poll 1: page 1 inserts 2 historical logs, then page 2 fails.
	if _, err := daemon.PollOnce(context.Background()); err == nil {
		t.Fatalf("expected PollOnce to fail on page 2")
	}

	// Poll 2: page 2 succeeds with empty page, so retry inserts 0 new logs (page 1 deduplicated),
	// yet pendingCatchupStart retained from Poll 1 must trigger historical aggregation for Nov 20.
	mockClient.failPage = 0
	currentTime = currentTime.Add(5 * time.Minute)
	cRes, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("retry PollOnce failed: %v", err)
	}
	if cRes.InsertedCount != 0 {
		t.Fatalf("expected 0 newly inserted logs on deduplicated retry, got %d", cRes.InsertedCount)
	}
	if daemon.SuccessfulAggregations() != 1 {
		t.Fatalf("expected retained historical catch-up aggregation to run once, got %d", daemon.SuccessfulAggregations())
	}

	dailyRows, err := QueryDailyUsage(context.Background(), db, tenantID, "2026-11-20", "2026-11-20")
	if err != nil {
		t.Fatalf("QueryDailyUsage failed: %v", err)
	}
	if len(dailyRows) != 1 || dailyRows[0].TotalTokens != 1200 {
		t.Fatalf("expected 2026-11-20 daily usage aggregated with 1200 tokens, got %+v", dailyRows)
	}
}

func TestMeteringDaemon_RecentInsertAfterIdleDoesNotReaggregatePartiallyPurgedDay(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupDaemonTestDB(t)

	// Scenario: Nov 10 had 1000 tokens total, already recorded in platform_tenant_usage_daily.
	// A later retention purge removed Nov 10 morning logs and left only one 18:00 log (200 tokens).
	// The system was idle until Nov 24, then restarted and ingested one fresh Nov 24 log.
	// Because no historical row was newly inserted on Nov 10, dynamic catch-up must NOT
	// re-aggregate Nov 10 and overwrite its complete 1000-token ledger total with 200.
	tenantID := "tenant-purged-day"
	nov10Evening := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	restartTime := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)

	db.Create(&entity.TenantUsageDaily{
		TenantID:    tenantID,
		ModelName:   "gpt-4o",
		UsageDate:   "2026-11-10",
		CallCount:   5,
		TotalTokens: 1000,
		TotalSpend:  0.10,
	})
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "nov10-survivor",
		TenantID:    tenantID,
		ModelName:   "gpt-4o",
		TotalTokens: 200,
		Spend:       0.02,
		RequestTime: nov10Evening,
	})

	mockClient := &timeFilteredSpendLogClient{
		entries: []gateway.SpendLogEntry{
			// Existing survivor echoed back inside the overlap window (must be skipped, not trigger catch-up).
			{
				SpendID:     "nov10-survivor",
				TeamID:      tenantID,
				Model:       "gpt-4o",
				TotalTokens: 200,
				Spend:       0.02,
				StartTime:   nov10Evening.Format("2006-01-02 15:04:05"),
			},
			// Only genuinely new log is on Nov 24 (within routine 48h lookback).
			{
				SpendID:     "nov24-fresh",
				TeamID:      tenantID,
				Model:       "gpt-4o",
				TotalTokens: 300,
				Spend:       0.03,
				StartTime:   time.Date(2026, 11, 24, 13, 55, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
			},
		},
	}

	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return restartTime }
	pipeline := NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	daemon := NewMeteringDaemon(collector, pipeline,
		WithOverlapWindow(5*time.Minute),
		WithLookbackHours(48),
	)
	daemon.nowFunc = func() time.Time { return restartTime }

	cRes, err := daemon.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}
	if cRes.InsertedCount != 1 {
		t.Fatalf("expected 1 newly inserted fresh log, got %d", cRes.InsertedCount)
	}
	if daemon.SuccessfulAggregations() != 0 {
		t.Fatalf("expected no historical catch-up aggregation when only today's log was inserted, got %d", daemon.SuccessfulAggregations())
	}

	// Nov 10 ledger total must remain intact at 1000 tokens / $0.10.
	rows, err := QueryDailyUsage(context.Background(), db, tenantID, "2026-11-10", "2026-11-10")
	if err != nil {
		t.Fatalf("QueryDailyUsage failed: %v", err)
	}
	if len(rows) != 1 || rows[0].TotalTokens != 1000 || math.Abs(rows[0].TotalSpend-0.10) > 1e-6 {
		t.Fatalf("partially purged Nov 10 ledger row was corrupted: %+v", rows)
	}
}
