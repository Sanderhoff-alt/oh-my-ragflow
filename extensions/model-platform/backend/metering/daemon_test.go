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
