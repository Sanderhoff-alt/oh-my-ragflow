package metering

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupPipelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("METERING_TIMEZONE", "")
	dbName := fmt.Sprintf("file:pipeline_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	ctx := context.Background()
	if err := migration.MigrateSpendLogSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate spend log schema: %v", err)
	}
	if err := migration.MigrateUsageSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate usage schema: %v", err)
	}

	return db
}

func TestUsagePipeline_DeterministicReplacementUpsert(t *testing.T) {
	db := setupPipelineTestDB(t)
	pipeline := NewUsagePipeline(db)

	d1 := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

	// Seed two raw logs on Day 1 for Tenant A (gpt-4o)
	logs := []entity.GatewaySpendLog{
		{
			SpendID:          "spend-001",
			TenantID:         "tenant-alpha",
			ModelName:        "gpt-4o",
			CallType:         "chat",
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CachedTokens:     80,
			ReasoningTokens:  30,
			Spend:            0.0015,
			CacheSavings:     0.0005,
			RequestTime:      d1,
		},
		{
			SpendID:          "spend-002",
			TenantID:         "tenant-alpha",
			ModelName:        "gpt-4o",
			CallType:         "chat",
			PromptTokens:     60,
			CompletionTokens: 40,
			TotalTokens:      100,
			CachedTokens:     50,
			ReasoningTokens:  20,
			Spend:            0.0010,
			CacheSavings:     0.0003,
			RequestTime:      d2,
		},
	}
	for _, l := range logs {
		if err := db.Create(&l).Error; err != nil {
			t.Fatalf("failed to seed spend log: %v", err)
		}
	}

	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 14, 23, 59, 59, 0, time.UTC)

	// First execution of pipeline
	res1, err := pipeline.AggregateDailyUsage(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregate run 1 failed: %v", err)
	}
	if res1.AggregatedRecords != 1 || res1.TenantsImpacted != 1 {
		t.Fatalf("expected 1 record and 1 tenant, got %+v", res1)
	}

	var row1 entity.TenantUsageDaily
	err = db.Where("tenant_id = ? AND model_name = ? AND usage_date = ?", "tenant-alpha", "gpt-4o", "2026-11-14").First(&row1).Error
	if err != nil {
		t.Fatalf("failed to find daily record: %v", err)
	}
	if row1.CallCount != 2 || row1.TotalTokens != 250 || row1.PromptTokens != 160 || row1.CompletionTokens != 90 {
		t.Errorf("unexpected row1 tokens: %+v", row1)
	}
	if row1.CachedTokens != 130 || row1.ReasoningTokens != 50 {
		t.Errorf("unexpected row1 detailed tokens: cached=%d, reasoning=%d", row1.CachedTokens, row1.ReasoningTokens)
	}
	if math.Abs(row1.CacheSavings-0.0008) > 1e-6 {
		t.Errorf("expected cache savings 0.0008, got %f", row1.CacheSavings)
	}
	if math.Abs(row1.TotalSpend-0.0025) > 1e-6 {
		t.Errorf("expected spend 0.0025, got %f", row1.TotalSpend)
	}

	// Second execution on identical time window (simulating 48h rolling recalculation)
	res2, err := pipeline.AggregateDailyUsage(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregate run 2 failed: %v", err)
	}
	if res2.AggregatedRecords != 1 {
		t.Fatalf("expected 1 record on run 2, got %+v", res2)
	}

	var row2 entity.TenantUsageDaily
	err = db.Where("tenant_id = ? AND model_name = ? AND usage_date = ?", "tenant-alpha", "gpt-4o", "2026-11-14").First(&row2).Error
	if err != nil {
		t.Fatalf("failed to find daily record after run 2: %v", err)
	}

	// CRITICAL ASSERTION: Zero doubling / inflation
	if row2.CallCount != 2 || row2.TotalTokens != 250 || row2.CachedTokens != 130 || row2.ReasoningTokens != 50 {
		t.Fatalf("Double counting detected on replay! CallCount=%d, TotalTokens=%d, Cached=%d",
			row2.CallCount, row2.TotalTokens, row2.CachedTokens)
	}
	if math.Abs(row2.TotalSpend-0.0025) > 1e-6 {
		t.Errorf("expected spend 0.0025 on replay, got %f", row2.TotalSpend)
	}

	// Add 1 more log to the window (late arrival log)
	newLog := entity.GatewaySpendLog{
		SpendID:          "spend-003",
		TenantID:         "tenant-alpha",
		ModelName:        "gpt-4o",
		CallType:         "chat",
		PromptTokens:     30,
		CompletionTokens: 20,
		TotalTokens:      50,
		Spend:            0.0005,
		RequestTime:      d1.Add(time.Hour),
	}
	db.Create(&newLog)

	// Third execution: values must reflect exact total (300 tokens, 3 calls, $0.0030) without accumulation defects
	_, err = pipeline.AggregateDailyUsage(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregate run 3 failed: %v", err)
	}

	var row3 entity.TenantUsageDaily
	if err := db.Where("tenant_id = ? AND model_name = ? AND usage_date = ?", "tenant-alpha", "gpt-4o", "2026-11-14").First(&row3).Error; err != nil {
		t.Fatalf("failed to find daily record after run 3: %v", err)
	}
	if row3.CallCount != 3 || row3.TotalTokens != 300 || row3.PromptTokens != 190 || row3.CompletionTokens != 110 {
		t.Fatalf("expected updated ground truth (3 calls, 300 tokens), got: %+v", row3)
	}
	if math.Abs(row3.TotalSpend-0.0030) > 1e-6 {
		t.Errorf("expected spend 0.0030, got %f", row3.TotalSpend)
	}
}

func TestUsagePipeline_CrossTenantAndCrossModelIsolation(t *testing.T) {
	db := setupPipelineTestDB(t)
	pipeline := NewUsagePipeline(db)

	reqTime := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	logs := []entity.GatewaySpendLog{
		{
			SpendID:     "s-iso-1",
			TenantID:    "tenant-1",
			ModelName:   "gpt-4o",
			TotalTokens: 100,
			Spend:       0.001,
			RequestTime: reqTime,
		},
		{
			SpendID:     "s-iso-2",
			TenantID:    "tenant-1",
			ModelName:   "text-embedding-3-small",
			TotalTokens: 50,
			Spend:       0.0001,
			RequestTime: reqTime,
		},
		{
			SpendID:     "s-iso-3",
			TenantID:    "tenant-2",
			ModelName:   "gpt-4o",
			TotalTokens: 200,
			Spend:       0.002,
			RequestTime: reqTime,
		},
	}
	for _, l := range logs {
		db.Create(&l)
	}

	start := reqTime.Add(-1 * time.Hour)
	end := reqTime.Add(1 * time.Hour)

	res, err := pipeline.AggregateDailyUsage(context.Background(), start, end)
	if err != nil {
		t.Fatalf("aggregation failed: %v", err)
	}
	if res.AggregatedRecords != 3 || res.TenantsImpacted != 2 {
		t.Fatalf("expected 3 records across 2 tenants, got %+v", res)
	}

	// Verify tenant-1 daily records
	t1Usage, err := QueryDailyUsage(context.Background(), db, "tenant-1", "2026-11-15", "2026-11-15")
	if err != nil {
		t.Fatalf("failed to query tenant-1 daily: %v", err)
	}
	if len(t1Usage) != 2 {
		t.Fatalf("expected 2 model records for tenant-1, got %d", len(t1Usage))
	}

	// Verify tenant-2 daily records
	t2Usage, err := QueryDailyUsage(context.Background(), db, "tenant-2", "2026-11-15", "2026-11-15")
	if err != nil {
		t.Fatalf("failed to query tenant-2 daily: %v", err)
	}
	if len(t2Usage) != 1 || t2Usage[0].TotalTokens != 200 {
		t.Fatalf("expected 1 record with 200 tokens for tenant-2, got %+v", t2Usage)
	}
}

func TestUsagePipeline_QueryUsageSummary(t *testing.T) {
	db := setupPipelineTestDB(t)

	// Direct daily records
	daily := []entity.TenantUsageDaily{
		{
			TenantID:         "tenant-summary",
			ModelName:        "gpt-4o",
			UsageDate:        "2026-11-10",
			CallCount:        5,
			PromptTokens:     500,
			CompletionTokens: 200,
			TotalTokens:      700,
			TotalSpend:       0.007,
		},
		{
			TenantID:         "tenant-summary",
			ModelName:        "gpt-4o",
			UsageDate:        "2026-11-11",
			CallCount:        3,
			PromptTokens:     300,
			CompletionTokens: 100,
			TotalTokens:      400,
			TotalSpend:       0.004,
		},
		{
			TenantID:         "tenant-summary",
			ModelName:        "text-embedding-3-small",
			UsageDate:        "2026-11-11",
			CallCount:        10,
			PromptTokens:     100,
			CompletionTokens: 0,
			TotalTokens:      100,
			TotalSpend:       0.0002,
		},
	}
	for _, d := range daily {
		db.Create(&d)
	}

	summary, err := QueryUsageSummary(context.Background(), db, "tenant-summary", "2026-11-10", "2026-11-11")
	if err != nil {
		t.Fatalf("query summary failed: %v", err)
	}

	if summary.TotalCalls != 18 {
		t.Errorf("expected 18 calls, got %d", summary.TotalCalls)
	}
	if summary.TotalTokens != 1200 {
		t.Errorf("expected 1200 total tokens, got %d", summary.TotalTokens)
	}
	if summary.ModelTokens["gpt-4o"] != 1100 {
		t.Errorf("expected 1100 tokens for gpt-4o, got %d", summary.ModelTokens["gpt-4o"])
	}
	if summary.ModelTokens["text-embedding-3-small"] != 100 {
		t.Errorf("expected 100 tokens for embedding, got %d", summary.ModelTokens["text-embedding-3-small"])
	}
}

func TestUsagePipeline_EmptyWindowAndError(t *testing.T) {
	db := setupPipelineTestDB(t)
	pipeline := NewUsagePipeline(db)

	// Empty window returns cleanly
	res, err := pipeline.AggregateDailyUsage(context.Background(), time.Now().Add(-10*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("expected nil error on empty window, got %v", err)
	}
	if res.AggregatedRecords != 0 || res.TenantsImpacted != 0 {
		t.Errorf("expected 0 records on empty window, got %+v", res)
	}

	// Nil DB returns error
	nilPipeline := NewUsagePipeline(nil)
	_, err = nilPipeline.AggregateDailyUsage(context.Background(), time.Now().Add(-10*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error for nil DB")
	}

	// Invalid time range: startTime after endTime
	_, err = pipeline.AggregateDailyUsage(context.Background(), time.Now(), time.Now().Add(-1*time.Hour))
	if err == nil {
		t.Fatal("expected error when startTime is after endTime")
	}
}

func TestUsagePipeline_RunWindow_RetainsEarlyMorningTokens(t *testing.T) {
	db := setupPipelineTestDB(t)
	pipeline := NewUsagePipeline(db)

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Two days ago (Day T-2)
	twoDaysAgo := today.AddDate(0, 0, -2)
	twoDaysAgoStr := twoDaysAgo.Format("2006-01-02")

	// Seed logs on Day T-2:
	// - Early morning log at 02:00:00 UTC (100 tokens)
	// - Afternoon log at 17:00:00 UTC (200 tokens)
	// - Evening log at 22:00:00 UTC (150 tokens)
	// Total for Day T-2 = 450 tokens, 3 calls.
	logs := []entity.GatewaySpendLog{
		{
			SpendID:          "s-t2-early",
			TenantID:         "tenant-rolling",
			ModelName:        "gpt-4o",
			TotalTokens:      100,
			PromptTokens:     80,
			CompletionTokens: 20,
			Spend:            0.0010,
			RequestTime:      twoDaysAgo.Add(2 * time.Hour), // 02:00 UTC
		},
		{
			SpendID:          "s-t2-afternoon",
			TenantID:         "tenant-rolling",
			ModelName:        "gpt-4o",
			TotalTokens:      200,
			PromptTokens:     150,
			CompletionTokens: 50,
			Spend:            0.0020,
			RequestTime:      twoDaysAgo.Add(17 * time.Hour), // 17:00 UTC
		},
		{
			SpendID:          "s-t2-evening",
			TenantID:         "tenant-rolling",
			ModelName:        "gpt-4o",
			TotalTokens:      150,
			PromptTokens:     100,
			CompletionTokens: 50,
			Spend:            0.0015,
			RequestTime:      twoDaysAgo.Add(22 * time.Hour), // 22:00 UTC
		},
		// Yesterday (Day T-1)
		{
			SpendID:          "s-t1-mid",
			TenantID:         "tenant-rolling",
			ModelName:        "gpt-4o",
			TotalTokens:      300,
			PromptTokens:     200,
			CompletionTokens: 100,
			Spend:            0.0030,
			RequestTime:      today.AddDate(0, 0, -1).Add(12 * time.Hour),
		},
	}
	for _, l := range logs {
		db.Create(&l)
	}

	// Trigger rolling 48-hour recalculation
	res, err := pipeline.RunWindow(context.Background(), 48)
	if err != nil {
		t.Fatalf("RunWindow failed: %v", err)
	}
	if res.AggregatedRecords < 2 {
		t.Fatalf("expected at least 2 aggregated daily records, got %d", res.AggregatedRecords)
	}

	// Verify Day T-2 daily record
	var t2Record entity.TenantUsageDaily
	err = db.Where("tenant_id = ? AND model_name = ? AND usage_date = ?", "tenant-rolling", "gpt-4o", twoDaysAgoStr).First(&t2Record).Error
	if err != nil {
		t.Fatalf("failed to find Day T-2 record (%s): %v", twoDaysAgoStr, err)
	}

	// Critical check: 02:00 UTC early morning log MUST NOT be truncated!
	if t2Record.CallCount != 3 || t2Record.TotalTokens != 450 {
		t.Fatalf("Early-morning truncation defect detected! Day T-2 has CallCount=%d, TotalTokens=%d (expected 3 calls, 450 tokens)",
			t2Record.CallCount, t2Record.TotalTokens)
	}
}

func TestUsagePipeline_QueryUsageSummary_Validation(t *testing.T) {
	db := setupPipelineTestDB(t)

	// Empty tenantID must return error
	_, err := QueryUsageSummary(context.Background(), db, "", "2026-11-01", "2026-11-30")
	if err == nil {
		t.Fatal("expected error for empty tenantID in QueryUsageSummary, got nil")
	}

	// QueryGlobalUsageSummary allows querying across all tenants
	summary, err := QueryGlobalUsageSummary(context.Background(), db, "2026-11-01", "2026-11-30")
	if err != nil {
		t.Fatalf("QueryGlobalUsageSummary failed: %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary, got nil")
	}
}

func TestUsagePipeline_ConfigurableTimezone(t *testing.T) {
	db := setupPipelineTestDB(t)

	// Log occurs at 2026-11-20 18:30:00 UTC.
	// In UTC: day is 2026-11-20.
	// In UTC+8 (Asia/Shanghai): day is 2026-11-21 02:30:00 (+08:00).
	reqUTC := time.Date(2026, 11, 20, 18, 30, 0, 0, time.UTC)
	log := entity.GatewaySpendLog{
		SpendID:          "spend-tz-1",
		TenantID:         "tenant-tz",
		ModelName:        "gpt-4o",
		CallType:         "chat",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Spend:            0.0015,
		RequestTime:      reqUTC,
	}
	if err := db.Create(&log).Error; err != nil {
		t.Fatalf("failed to insert spend log: %v", err)
	}

	// 1. Run with UTC timezone
	utcPipeline := NewUsagePipeline(db, WithPipelineTimezone(time.UTC))
	startUTC := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	endUTC := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)

	resUTC, err := utcPipeline.AggregateDailyUsage(context.Background(), startUTC, endUTC)
	if err != nil {
		t.Fatalf("UTC aggregation failed: %v", err)
	}
	if resUTC.AggregatedRecords != 1 {
		t.Fatalf("expected 1 aggregated record for UTC, got %d", resUTC.AggregatedRecords)
	}

	var utcRow entity.TenantUsageDaily
	err = db.Where("tenant_id = ? AND usage_date = ?", "tenant-tz", "2026-11-20").First(&utcRow).Error
	if err != nil {
		t.Fatalf("expected record on 2026-11-20 for UTC pipeline: %v", err)
	}
	if utcRow.TotalTokens != 150 || utcRow.CallCount != 1 {
		t.Errorf("unexpected UTC row totals: %+v", utcRow)
	}

	// Clean up table for second test
	db.Exec("DELETE FROM platform_tenant_usage_daily")

	// 2. Run with UTC+8 timezone
	beijingLoc := time.FixedZone("CST", 8*3600)
	cstPipeline := NewUsagePipeline(db, WithPipelineTimezone(beijingLoc))
	startCST := time.Date(2026, 11, 21, 0, 0, 0, 0, beijingLoc)
	endCST := time.Date(2026, 11, 22, 0, 0, 0, 0, beijingLoc)

	resCST, err := cstPipeline.AggregateDailyUsage(context.Background(), startCST, endCST)
	if err != nil {
		t.Fatalf("CST aggregation failed: %v", err)
	}
	if resCST.AggregatedRecords != 1 {
		t.Fatalf("expected 1 aggregated record for CST, got %d", resCST.AggregatedRecords)
	}

	var cstRow entity.TenantUsageDaily
	err = db.Where("tenant_id = ? AND usage_date = ?", "tenant-tz", "2026-11-21").First(&cstRow).Error
	if err != nil {
		t.Fatalf("expected record on 2026-11-21 for CST pipeline: %v", err)
	}
	if cstRow.TotalTokens != 150 || cstRow.CallCount != 1 {
		t.Errorf("unexpected CST row totals: %+v", cstRow)
	}
}

func TestUsagePipeline_ResolveMeteringLocation(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")

	// Empty / default -> UTC
	if loc := ResolveMeteringLocation(""); loc != time.UTC {
		t.Errorf("expected UTC for empty string, got %v", loc)
	}
	if loc := ResolveMeteringLocation("UTC"); loc != time.UTC {
		t.Errorf("expected UTC for 'UTC', got %v", loc)
	}

	// Numeric offsets
	loc8 := ResolveMeteringLocation("+08:00")
	_, off8 := time.Now().In(loc8).Zone()
	if off8 != 28800 {
		t.Errorf("expected 28800 for +08:00, got %d", off8)
	}

	loc5 := ResolveMeteringLocation("-05:00")
	_, off5 := time.Now().In(loc5).Zone()
	if off5 != -18000 {
		t.Errorf("expected -18000 for -05:00, got %d", off5)
	}

	// Invalid fallback to UTC
	locInvalid := ResolveMeteringLocation("invalid/unknown/tz")
	if locInvalid != time.UTC {
		t.Errorf("expected fallback to UTC for invalid tz, got %v", locInvalid)
	}

	// Malformed numeric offsets fallback to UTC
	if loc := ResolveMeteringLocation("+oops:30"); loc != time.UTC {
		t.Errorf("expected UTC for '+oops:30', got %v", loc)
	}
	if loc := ResolveMeteringLocation("+25:00"); loc != time.UTC {
		t.Errorf("expected UTC for '+25:00', got %v", loc)
	}
	if loc := ResolveMeteringLocation("-05:70"); loc != time.UTC {
		t.Errorf("expected UTC for '-05:70', got %v", loc)
	}
}

type mockDialectorWithName struct {
	gorm.Dialector
	name string
}

func (m mockDialectorWithName) Name() string {
	return m.name
}

func TestUsagePipeline_ResolveDateExpression(t *testing.T) {
	utcLoc := time.UTC
	beijingLoc := time.FixedZone("CST", 8*3600)

	// SQLite
	sqliteDB := &gorm.DB{Config: &gorm.Config{Dialector: mockDialectorWithName{name: "sqlite"}}}
	exprSQLiteUTC := resolveDateExpression(sqliteDB, utcLoc)
	if exprSQLiteUTC != "strftime('%Y-%m-%d', request_time)" {
		t.Errorf("unexpected SQLite UTC expr: %s", exprSQLiteUTC)
	}
	exprSQLiteCST := resolveDateExpression(sqliteDB, beijingLoc)
	if exprSQLiteCST != "strftime('%Y-%m-%d', datetime(request_time, '+28800 seconds'))" {
		t.Errorf("unexpected SQLite CST expr: %s", exprSQLiteCST)
	}

	// PostgreSQL
	pgDB := &gorm.DB{Config: &gorm.Config{Dialector: mockDialectorWithName{name: "postgres"}}}
	exprPGUTC := resolveDateExpression(pgDB, utcLoc)
	if exprPGUTC != "to_char(request_time AT TIME ZONE 'UTC', 'YYYY-MM-DD')" {
		t.Errorf("unexpected PG UTC expr: %s", exprPGUTC)
	}
	exprPGCST := resolveDateExpression(pgDB, beijingLoc)
	if exprPGCST != "to_char(request_time AT TIME ZONE INTERVAL '+08:00', 'YYYY-MM-DD')" {
		t.Errorf("unexpected PG CST expr: %s", exprPGCST)
	}
	londonLoc, err := time.LoadLocation("Europe/London")
	if err == nil {
		exprPGLondon := resolveDateExpression(pgDB, londonLoc)
		if exprPGLondon != "to_char(request_time AT TIME ZONE 'Europe/London', 'YYYY-MM-DD')" {
			t.Errorf("unexpected PG London expr: %s", exprPGLondon)
		}
	}

	// MySQL
	mysqlDB := &gorm.DB{Config: &gorm.Config{Dialector: mockDialectorWithName{name: "mysql"}}}
	exprMySQLUTC := resolveDateExpression(mysqlDB, utcLoc)
	if exprMySQLUTC != "DATE_FORMAT(request_time, '%Y-%m-%d')" {
		t.Errorf("unexpected MySQL UTC expr: %s", exprMySQLUTC)
	}
	exprMySQLCST := resolveDateExpression(mysqlDB, beijingLoc)
	if exprMySQLCST != "DATE_FORMAT(CONVERT_TZ(request_time, '+00:00', '+08:00'), '%Y-%m-%d')" {
		t.Errorf("unexpected MySQL CST expr: %s", exprMySQLCST)
	}
}
