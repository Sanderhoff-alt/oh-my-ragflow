package metering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

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
	daemon := NewMeteringDaemon(db, collector, pipeline,
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

	daemon := NewMeteringDaemon(db, collector, pipeline,
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

	daemon := NewMeteringDaemon(db, collector, pipeline,
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

	daemon := NewMeteringDaemon(db, collector, pipeline)

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
	nilPipelineDaemon := NewMeteringDaemon(db, collector, nil)
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

	daemon := NewMeteringDaemon(db, collector, pipeline,
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

	daemon := NewMeteringDaemon(db, collector, pipeline)

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

			daemon := NewMeteringDaemon(db, collector, pipeline)
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

	daemon := NewMeteringDaemon(db, collector, pipeline,
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

	daemon := NewMeteringDaemon(db, collector, pipeline,
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
	nilPipelineDaemon := NewMeteringDaemon(db, collector, nil, WithRetentionDays(90))
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

	daemon := NewMeteringDaemon(db, collector, pipeline, WithRetentionDays(90))
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
