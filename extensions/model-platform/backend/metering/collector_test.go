package metering

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type mockSpendLogClient struct {
	entries []gateway.SpendLogEntry
	err     error
	lastReq gateway.SpendLogQueryParams
}

func (m *mockSpendLogClient) GetSpendLogs(ctx context.Context, params gateway.SpendLogQueryParams) ([]gateway.SpendLogEntry, error) {
	m.lastReq = params
	if m.err != nil {
		return nil, m.err
	}
	if params.Page > 0 && params.PageSize > 0 {
		start := (params.Page - 1) * params.PageSize
		if start >= len(m.entries) {
			return []gateway.SpendLogEntry{}, nil
		}
		end := start + params.PageSize
		if end > len(m.entries) {
			end = len(m.entries)
		}
		return m.entries[start:end], nil
	}
	return m.entries, nil
}

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:collector_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}

	if err := migration.MigrateSpendLogSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate spend log schema: %v", err)
	}

	return db
}

func TestSpendLogCollector_Deduplication(t *testing.T) {
	db := setupTestDB(t)

	mockClient := &mockSpendLogClient{
		entries: []gateway.SpendLogEntry{
			{
				SpendID:          "spend-101",
				Model:            "gpt-4o",
				TeamID:           "tenant_alpha",
				TotalTokens:      100,
				PromptTokens:     80,
				CompletionTokens: 20,
				Spend:            0.0015,
				StartTime:        "2026-11-15 10:00:00",
			},
			{
				SpendID:          "spend-102",
				Model:            "text-embedding-3-small",
				TeamID:           "tenant_alpha",
				TotalTokens:      40,
				PromptTokens:     40,
				CompletionTokens: 0,
				Spend:            0.0001,
				StartTime:        "2026-11-15 10:05:00",
			},
			{
				SpendID:          "spend-103",
				Model:            "gpt-4o",
				TeamID:           "tenant_beta",
				TotalTokens:      250,
				PromptTokens:     200,
				CompletionTokens: 50,
				Spend:            0.0035,
				StartTime:        "2026-11-15 10:10:00",
			},
		},
	}

	collector := NewSpendLogCollector(db, mockClient)
	start := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	// First collection run: 3 entries inserted
	res1, err := collector.CollectSpendLogs(context.Background(), start, end)
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if res1.TotalFetched != 3 || res1.InsertedCount != 3 || res1.SkippedCount != 0 {
		t.Errorf("unexpected res1: %+v", res1)
	}

	var count1 int64
	db.Model(&entity.GatewaySpendLog{}).Count(&count1)
	if count1 != 3 {
		t.Fatalf("expected 3 rows in database, got %d", count1)
	}

	// Second collection run with exact same entries: physical deduplication must yield 0 inserted, 3 skipped
	res2, err := collector.CollectSpendLogs(context.Background(), start, end)
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if res2.TotalFetched != 3 || res2.InsertedCount != 0 || res2.SkippedCount != 3 {
		t.Errorf("expected 0 inserted and 3 skipped on duplicate run, got: %+v", res2)
	}

	var count2 int64
	db.Model(&entity.GatewaySpendLog{}).Count(&count2)
	if count2 != 3 {
		t.Fatalf("row count must remain 3, got %d (deduplication failed)", count2)
	}

	// Third run: 3 existing entries + 1 new entry
	mockClient.entries = append(mockClient.entries, gateway.SpendLogEntry{
		SpendID:          "spend-104",
		Model:            "gpt-4o",
		TeamID:           "tenant_gamma",
		TotalTokens:      300,
		PromptTokens:     250,
		CompletionTokens: 50,
		Spend:            0.0045,
		StartTime:        "2026-11-15 11:00:00",
	})

	res3, err := collector.CollectSpendLogs(context.Background(), start, end)
	if err != nil {
		t.Fatalf("third run failed: %v", err)
	}
	if res3.TotalFetched != 4 || res3.InsertedCount != 1 || res3.SkippedCount != 3 {
		t.Errorf("expected 1 inserted and 3 skipped, got: %+v", res3)
	}

	var count3 int64
	db.Model(&entity.GatewaySpendLog{}).Count(&count3)
	if count3 != 4 {
		t.Fatalf("expected 4 rows in database, got %d", count3)
	}
}

func TestSpendLogCollector_TenantIDResolution(t *testing.T) {
	tests := []struct {
		name     string
		entry    gateway.SpendLogEntry
		expected string
	}{
		{
			name: "from team_id with prefix",
			entry: gateway.SpendLogEntry{
				TeamID: "tenant_abc123",
			},
			expected: "abc123",
		},
		{
			name: "from metadata user_api_key_team_id",
			entry: gateway.SpendLogEntry{
				Metadata: map[string]interface{}{
					"user_api_key_team_id": "tenant_def456",
				},
			},
			expected: "def456",
		},
		{
			name: "key alias is not a tenant source",
			entry: gateway.SpendLogEntry{
				Metadata: map[string]interface{}{
					"user_api_key_alias": "tenant_ghi789_1a2b3c4d",
				},
			},
			expected: "",
		},
		{
			name: "from metadata tenant_id",
			entry: gateway.SpendLogEntry{
				Metadata: map[string]interface{}{
					"tenant_id": "jkl012",
				},
			},
			expected: "jkl012",
		},
		{
			name: "from user with prefix",
			entry: gateway.SpendLogEntry{
				User: "tenant_mno345",
			},
			expected: "mno345",
		},
		{
			name: "untrusted user without tenant prefix is rejected",
			entry: gateway.SpendLogEntry{
				User: "direct_user_id",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ExtractTenantID(tt.entry.TeamID, tt.entry.User, tt.entry.Metadata)
			if actual != tt.expected {
				t.Errorf("ExtractTenantID() = %q, want %q", actual, tt.expected)
			}
		})
	}
}

func TestSpendLogCollector_RequestTimeParsing(t *testing.T) {
	tests := []struct {
		startTime string
		endTime   string
		expectedY int
		expectedM time.Month
		expectedD int
	}{
		{
			startTime: "2026-11-15 14:30:00",
			expectedY: 2026,
			expectedM: time.November,
			expectedD: 15,
		},
		{
			startTime: "2026-11-15T14:30:00Z",
			expectedY: 2026,
			expectedM: time.November,
			expectedD: 15,
		},
		{
			startTime: "2026-11-15",
			expectedY: 2026,
			expectedM: time.November,
			expectedD: 15,
		},
		{
			startTime: "",
			endTime:   "2026-11-14 20:00:00",
			expectedY: 2026,
			expectedM: time.November,
			expectedD: 14,
		},
		{
			startTime: "invalid-timestamp-string",
			endTime:   "2026-11-14 20:00:00",
			expectedY: 2026,
			expectedM: time.November,
			expectedD: 14,
		},
		{
			startTime: "invalid-timestamp-string",
			endTime:   "invalid-end-time",
			expectedY: 1970,
			expectedM: time.January,
			expectedD: 1,
		},
	}

	for _, tt := range tests {
		parsed := ParseRequestTime(tt.startTime, tt.endTime)
		if parsed.Year() != tt.expectedY || parsed.Month() != tt.expectedM || parsed.Day() != tt.expectedD {
			t.Errorf("ParseRequestTime(%q, %q) = %v, expected date %d-%02d-%02d",
				tt.startTime, tt.endTime, parsed, tt.expectedY, tt.expectedM, tt.expectedD)
		}
	}
}

func TestSpendLogCollector_QueryRawSpendLogs(t *testing.T) {
	db := setupTestDB(t)

	now := time.Now().UTC()
	logs := []entity.GatewaySpendLog{
		{
			SpendID:     "s-query-1",
			TenantID:    "t-1",
			ModelName:   "gpt-4o",
			TotalTokens: 100,
			Spend:       0.001,
			RequestTime: now.Add(-2 * time.Hour),
		},
		{
			SpendID:     "s-query-2",
			TenantID:    "t-1",
			ModelName:   "text-embedding-3-small",
			TotalTokens: 50,
			Spend:       0.0001,
			RequestTime: now.Add(-1 * time.Hour),
		},
		{
			SpendID:     "s-query-3",
			TenantID:    "t-2",
			ModelName:   "gpt-4o",
			TotalTokens: 200,
			Spend:       0.002,
			RequestTime: now,
		},
	}

	for _, l := range logs {
		db.Create(&l)
	}

	// Query for tenant t-1 only
	results, total, err := QueryRawSpendLogs(context.Background(), db, "t-1", nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if total != 2 || len(results) != 2 {
		t.Fatalf("expected 2 records for t-1, got total=%d len=%d", total, len(results))
	}

	// Query with time range
	tMinus90Min := now.Add(-90 * time.Minute)
	resultsTime, totalTime, err := QueryRawSpendLogs(context.Background(), db, "", &tMinus90Min, nil, 10, 0)
	if err != nil {
		t.Fatalf("time query failed: %v", err)
	}
	if totalTime != 2 || len(resultsTime) != 2 {
		t.Fatalf("expected 2 records within last 90 minutes, got total=%d len=%d", totalTime, len(resultsTime))
	}
}

func TestSpendLogCollector_EmptyAndErrorHandling(t *testing.T) {
	db := setupTestDB(t)

	// Error returned from gateway
	errClient := &mockSpendLogClient{
		err: errors.New("connection refused: proxy down"),
	}
	collector := NewSpendLogCollector(db, errClient)

	_, err := collector.CollectSpendLogs(context.Background(), time.Now().Add(-1*time.Hour), time.Now())
	if err == nil {
		t.Fatal("expected error on gateway failure, got nil")
	}

	// Nil DB and nil client validation
	nilDBCollector := NewSpendLogCollector(nil, errClient)
	if _, err := nilDBCollector.CollectSpendLogs(context.Background(), time.Now(), time.Now()); err == nil {
		t.Fatal("expected error for nil DB")
	}

	nilClientCollector := NewSpendLogCollector(db, nil)
	if _, err := nilClientCollector.CollectSpendLogs(context.Background(), time.Now(), time.Now()); err == nil {
		t.Fatal("expected error for nil client")
	}

	// Empty list returned from gateway
	emptyClient := &mockSpendLogClient{entries: []gateway.SpendLogEntry{}}
	collectorEmpty := NewSpendLogCollector(db, emptyClient)
	res, err := collectorEmpty.CollectSpendLogs(context.Background(), time.Now().Add(-1*time.Hour), time.Now())
	if err != nil {
		t.Fatalf("unexpected error for empty list: %v", err)
	}
	if res.TotalFetched != 0 || res.InsertedCount != 0 || res.SkippedCount != 0 {
		t.Errorf("expected 0 for all counts, got %+v", res)
	}
}

func TestSpendLogCollector_TokenDetailsExtraction(t *testing.T) {
	entry1 := gateway.SpendLogEntry{
		SpendID:          "spend-cache-1",
		Model:            "deepseek-r1",
		TeamID:           "tenant_test",
		TotalTokens:      10000,
		PromptTokens:     9000,
		CompletionTokens: 1000,
		Spend:            0.015,
		PromptTokensDetails: &gateway.PromptTokensDetails{
			CachedTokens: 8000,
		},
		CompletionTokensDetails: &gateway.CompletionTokensDetails{
			ReasoningTokens: 900,
		},
		CacheCreationTokens: 500,
		CacheSavings:        0.0112,
		PromptSpend:         0.005,
		CompletionSpend:     0.010,
	}

	log1 := ConvertEntryToSpendLog(entry1)
	if log1.CachedTokens != 8000 {
		t.Errorf("expected CachedTokens 8000, got %d", log1.CachedTokens)
	}
	if log1.ReasoningTokens != 900 {
		t.Errorf("expected ReasoningTokens 900, got %d", log1.ReasoningTokens)
	}
	if log1.CacheWriteTokens != 500 {
		t.Errorf("expected CacheWriteTokens 500, got %d", log1.CacheWriteTokens)
	}
	if log1.CacheSavings != 0.0112 {
		t.Errorf("expected CacheSavings 0.0112, got %f", log1.CacheSavings)
	}

	// Test metadata extraction fallback
	entry2 := gateway.SpendLogEntry{
		SpendID:          "spend-cache-2",
		Model:            "claude-3-5-sonnet",
		TeamID:           "tenant_test",
		TotalTokens:      5000,
		PromptTokens:     4000,
		CompletionTokens: 1000,
		Spend:            0.02,
		Metadata: map[string]interface{}{
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": float64(3500),
			},
			"completion_tokens_details": map[string]interface{}{
				"reasoning_tokens": float64(450),
			},
			"cache_creation_input_tokens": float64(200),
			"saved_cache_cost":            float64(0.0084),
		},
	}

	log2 := ConvertEntryToSpendLog(entry2)
	if log2.CachedTokens != 3500 {
		t.Errorf("expected CachedTokens 3500 from metadata, got %d", log2.CachedTokens)
	}
	if log2.ReasoningTokens != 450 {
		t.Errorf("expected ReasoningTokens 450 from metadata, got %d", log2.ReasoningTokens)
	}
	if log2.CacheWriteTokens != 200 {
		t.Errorf("expected CacheWriteTokens 200 from metadata, got %d", log2.CacheWriteTokens)
	}
	if log2.CacheSavings != 0.0084 {
		t.Errorf("expected CacheSavings 0.0084 from metadata, got %f", log2.CacheSavings)
	}

	// Test metadata empty direct map fallback to usage_object
	entry3 := gateway.SpendLogEntry{
		SpendID: "spend-cache-3",
		Model:   "o3-mini",
		Metadata: map[string]interface{}{
			"prompt_tokens_details":     map[string]interface{}{},
			"completion_tokens_details": map[string]interface{}{},
			"usage_object": map[string]interface{}{
				"prompt_tokens_details": map[string]interface{}{
					"cached_tokens": float64(1200),
				},
				"completion_tokens_details": map[string]interface{}{
					"reasoning_tokens": float64(800),
				},
			},
		},
	}
	log3 := ConvertEntryToSpendLog(entry3)
	if log3.CachedTokens != 1200 {
		t.Errorf("expected CachedTokens 1200 from usage_object fallback, got %d", log3.CachedTokens)
	}
	if log3.ReasoningTokens != 800 {
		t.Errorf("expected ReasoningTokens 800 from usage_object fallback, got %d", log3.ReasoningTokens)
	}
}

func TestSpendLogCollector_TTSCharacterExtraction(t *testing.T) {
	// 1. Explicit metadata.characters
	ttsMeta := ConvertEntryToSpendLog(gateway.SpendLogEntry{
		SpendID:  "spend-tts-meta",
		Model:    "tts-1",
		CallType: "speech",
		Metadata: map[string]interface{}{"characters": float64(1350)},
	})
	if ttsMeta.Characters != 1350 {
		t.Errorf("expected 1350 characters from metadata, got %d", ttsMeta.Characters)
	}

	// 2. Fallback to prompt_tokens for speech call_type when metadata omits characters
	ttsFallback := ConvertEntryToSpendLog(gateway.SpendLogEntry{
		SpendID:      "spend-tts-fallback",
		Model:        "tts-1-hd",
		CallType:     "aspeech",
		PromptTokens: 640,
		TotalTokens:  640,
	})
	if ttsFallback.Characters != 640 {
		t.Errorf("expected 640 characters fallback for aspeech, got %d", ttsFallback.Characters)
	}

	// 3. Regular chat call must not copy prompt_tokens into Characters
	chatLog := ConvertEntryToSpendLog(gateway.SpendLogEntry{
		SpendID:      "spend-chat-no-chars",
		Model:        "deepseek-v3",
		CallType:     "completion",
		PromptTokens: 900,
		TotalTokens:  1200,
	})
	if chatLog.Characters != 0 {
		t.Errorf("expected 0 characters for chat completion, got %d", chatLog.Characters)
	}
}

func TestSpendLogCollector_FallbackHashDifferentiationAndReplay(t *testing.T) {
	db := setupTestDB(t)

	mockClient := &mockSpendLogClient{
		entries: []gateway.SpendLogEntry{
			{
				APIKey:      "key-1",
				Model:       "gpt-4o",
				EndTime:     "2026-11-15 10:00:00",
				CallType:    "completion",
				TotalTokens: 100,
				Spend:       0.0015,
				TeamID:      "tenant_t1",
			},
			{
				APIKey:      "key-1",
				Model:       "gpt-4o",
				EndTime:     "2026-11-15 10:05:00", // Differs only by EndTime
				CallType:    "completion",
				TotalTokens: 100,
				Spend:       0.0015,
				TeamID:      "tenant_t1",
			},
		},
	}

	collector := NewSpendLogCollector(db, mockClient)
	ctx := context.Background()

	now := time.Now()
	res, err := collector.CollectSpendLogs(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.InsertedCount != 2 {
		t.Fatalf("expected 2 inserted logs for entries differing by EndTime, got %d", res.InsertedCount)
	}

	// Replay exact same logs: both should be skipped
	resReplay, err := collector.CollectSpendLogs(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatalf("unexpected error on replay: %v", err)
	}
	if resReplay.InsertedCount != 0 {
		t.Fatalf("expected 0 inserted logs on exact replay, got %d", resReplay.InsertedCount)
	}
	if resReplay.SkippedCount != 2 {
		t.Fatalf("expected 2 skipped logs on exact replay, got %d", resReplay.SkippedCount)
	}
}

func TestSpendLogCollector_SubsecondEndDateRounding(t *testing.T) {
	db := setupTestDB(t)

	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)

	// End time with fractional seconds (10:00:01.500)
	start := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	endWithFraction := time.Date(2026, 11, 15, 10, 0, 1, 500000000, time.UTC)

	_, err := collector.CollectSpendLogs(context.Background(), start, endWithFraction)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should round up to 10:00:02 to capture inclusive subsecond window
	expectedEndDate := "2026-11-15 10:00:02"
	if mockClient.lastReq.EndDate != expectedEndDate {
		t.Errorf("expected EndDate %q, got %q", expectedEndDate, mockClient.lastReq.EndDate)
	}

	// End time with zero nanoseconds should preserve exact second
	endExact := time.Date(2026, 11, 15, 10, 0, 1, 0, time.UTC)
	_, _ = collector.CollectSpendLogs(context.Background(), start, endExact)
	if mockClient.lastReq.EndDate != "2026-11-15 10:00:01" {
		t.Errorf("expected exact EndDate '2026-11-15 10:00:01', got %q", mockClient.lastReq.EndDate)
	}
}

func TestSpendLogCollector_QueryRawSpendLogs_DeterministicTieBreaker(t *testing.T) {
	db := setupTestDB(t)

	identicalTime := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "spend-alpha",
		TenantID:    "t-tie",
		RequestTime: identicalTime,
	})
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "spend-beta",
		TenantID:    "t-tie",
		RequestTime: identicalTime,
	})

	results, _, err := QueryRawSpendLogs(context.Background(), db, "t-tie", nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// spend-beta should come before spend-alpha due to spend_id DESC tie-breaker
	if results[0].SpendID != "spend-beta" || results[1].SpendID != "spend-alpha" {
		t.Errorf("expected deterministic tie-breaker order [spend-beta, spend-alpha], got [%s, %s]",
			results[0].SpendID, results[1].SpendID)
	}
}

func TestSpendLogCollector_FallbackHash_UnambiguousEncoding(t *testing.T) {
	// Verify that fields containing delimiter-like characters do not collide
	log1 := ConvertEntryToSpendLog(gateway.SpendLogEntry{
		TeamID:      "a|b",
		APIKey:      "c",
		Model:       "gpt-4o",
		CallType:    "chat",
		TotalTokens: 100,
		Spend:       0.01,
	})

	log2 := ConvertEntryToSpendLog(gateway.SpendLogEntry{
		TeamID:      "a",
		APIKey:      "b|c",
		Model:       "gpt-4o",
		CallType:    "chat",
		TotalTokens: 100,
		Spend:       0.01,
	})

	if log1.SpendID == log2.SpendID {
		t.Fatalf("expected different spend IDs for ('a|b', 'c') vs ('a', 'b|c'), but got identical hash %s", log1.SpendID)
	}
}

func TestSpendLogCollector_Pagination(t *testing.T) {
	db := setupTestDB(t)

	// Create 5 log entries
	var mockEntries []gateway.SpendLogEntry
	for i := 1; i <= 5; i++ {
		mockEntries = append(mockEntries, gateway.SpendLogEntry{
			SpendID:     fmt.Sprintf("spend-paged-%d", i),
			TeamID:      "tenant-paged",
			Model:       "gpt-4o",
			TotalTokens: 100 * i,
			Spend:       0.01 * float64(i),
			StartTime:   "2026-11-15 10:00:00",
		})
	}

	mockClient := &mockSpendLogClient{entries: mockEntries}
	// Configure batch size of 2 to force 3 pages (2 + 2 + 1)
	collector := NewSpendLogCollector(db, mockClient, WithBatchSize(2))

	startTime := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

	res, err := collector.CollectSpendLogs(context.Background(), startTime, endTime)
	if err != nil {
		t.Fatalf("unexpected error during paginated collection: %v", err)
	}

	if res.TotalFetched != 5 {
		t.Fatalf("expected 5 total fetched across pages, got %d", res.TotalFetched)
	}
	if res.InsertedCount != 5 {
		t.Fatalf("expected 5 inserted logs, got %d", res.InsertedCount)
	}
	if res.SkippedCount != 0 {
		t.Fatalf("expected 0 skipped, got %d", res.SkippedCount)
	}

	// Verify all 5 entries exist in database
	var count int64
	db.Model(&entity.GatewaySpendLog{}).Where("tenant_id = ?", "tenant-paged").Count(&count)
	if count != 5 {
		t.Fatalf("expected 5 records in database, found %d", count)
	}
}

func TestSpendLogCollector_GetLatestRequestTime(t *testing.T) {
	db := setupTestDB(t)
	mockClient := &mockSpendLogClient{}
	collector := NewSpendLogCollector(db, mockClient)

	// Case 1: Empty table returns nil
	latest, err := collector.GetLatestRequestTime(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on empty table: %v", err)
	}
	if latest != nil {
		t.Fatalf("expected nil for empty table, got %v", latest)
	}

	// Case 2: Insert two records
	t1 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 22, 15, 30, 0, 0, time.UTC)

	db.Create(&entity.GatewaySpendLog{
		SpendID:     "s1",
		TenantID:    "t1",
		RequestTime: t1,
	})
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "s2",
		TenantID:    "t1",
		RequestTime: t2,
	})

	latest, err = collector.GetLatestRequestTime(context.Background())
	if err != nil {
		t.Fatalf("unexpected error querying latest time: %v", err)
	}
	if latest == nil {
		t.Fatalf("expected non-nil latest time")
	}
	if !latest.Equal(t2) {
		t.Fatalf("expected latest time %v, got %v", t2, *latest)
	}
}

func TestSpendLogCollector_CollectIncremental_ColdStartDayAligned(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")

	// 1. Cold start in UTC: 2026-11-24 15:30:00 UTC with 48h lookback -> 2026-11-22 00:00:00 UTC
	dbUTC := setupTestDB(t)
	mockUTC := &mockSpendLogClient{}
	fixedNow := time.Date(2026, 11, 24, 15, 30, 0, 0, time.UTC)

	collectorUTC := NewSpendLogCollector(dbUTC, mockUTC, WithCollectorTimezone(time.UTC))
	collectorUTC.nowFunc = func() time.Time { return fixedNow }

	res, err := collectorUTC.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("unexpected error during UTC cold-start collection: %v", err)
	}
	expectedUTC := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	if !res.StartTime.Equal(expectedUTC) {
		t.Fatalf("expected UTC cold start %v, got %v", expectedUTC, res.StartTime)
	}
	if mockUTC.lastReq.StartDate != "2026-11-22 00:00:00" {
		t.Fatalf("expected gateway StartDate 2026-11-22 00:00:00, got %s", mockUTC.lastReq.StartDate)
	}

	// 2. Cold start in UTC+08:00: 2026-11-24 02:30:00 +08:00 (2026-11-23 18:30:00 UTC)
	//    48h earlier in +08:00 is 2026-11-22 02:30:00 +08:00
	//    day-aligned in +08:00 is 2026-11-22 00:00:00 +08:00 == 2026-11-21 16:00:00 UTC.
	dbCST := setupTestDB(t)
	mockCST := &mockSpendLogClient{}
	cst := time.FixedZone("UTC+08:00", 8*3600)
	fixedNowCST := time.Date(2026, 11, 24, 2, 30, 0, 0, cst)

	collectorCST := NewSpendLogCollector(dbCST, mockCST, WithCollectorTimezone(cst))
	collectorCST.nowFunc = func() time.Time { return fixedNowCST }

	resCST, err := collectorCST.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("unexpected error during CST cold-start collection: %v", err)
	}
	expectedCSTInUTC := time.Date(2026, 11, 21, 16, 0, 0, 0, time.UTC)
	if !resCST.StartTime.Equal(expectedCSTInUTC) {
		t.Fatalf("expected CST day-aligned cold start %v, got %v", expectedCSTInUTC, resCST.StartTime)
	}
	if mockCST.lastReq.StartDate != "2026-11-21 16:00:00" {
		t.Fatalf("expected gateway StartDate 2026-11-21 16:00:00, got %s", mockCST.lastReq.StartDate)
	}
	startInCST := resCST.StartTime.In(cst)
	if startInCST.Hour() != 0 || startInCST.Minute() != 0 || startInCST.Second() != 0 {
		t.Fatalf("expected 00:00:00 in target timezone, got %v", startInCST)
	}
}

func TestSpendLogCollector_CollectIncremental_IdleWindowInvariant(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")
	db := setupTestDB(t)
	mockClient := &mockSpendLogClient{} // always returns 0 entries

	currentTime := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }

	// Seed a single old log 10 hours ago so DB watermark stays frozen in the past.
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "idle-seed-1",
		TenantID:    "t-idle",
		RequestTime: currentTime.Add(-10 * time.Hour),
	})

	// Cycle 1 (startup with existing DB record): starts at watermark - 5m and establishes checkpoint.
	res1, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}
	if !res1.StartTime.Equal(currentTime.Add(-10*time.Hour - 5*time.Minute)) {
		t.Fatalf("expected cycle 1 start at watermark-5m, got %v", res1.StartTime)
	}
	if !collector.lastCheckedAt.Equal(currentTime) {
		t.Fatalf("expected checkpoint %v after cycle 1, got %v", currentTime, collector.lastCheckedAt)
	}

	// Cycles 2..6: advance clock by 5 minutes repeatedly with 0 new logs; window must stay bounded at 10m.
	for i := 2; i <= 6; i++ {
		prevTime := currentTime
		currentTime = currentTime.Add(5 * time.Minute)

		res, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
		if err != nil {
			t.Fatalf("cycle %d failed: %v", i, err)
		}
		expectedStart := prevTime.Add(-5 * time.Minute)
		if !res.StartTime.Equal(expectedStart) {
			t.Fatalf("cycle %d: expected start %v, got %v", i, expectedStart, res.StartTime)
		}
		windowSpan := res.EndTime.Sub(res.StartTime)
		if windowSpan != 10*time.Minute {
			t.Fatalf("cycle %d: expected bounded 10m window, got %v", i, windowSpan)
		}
		if !collector.lastCheckedAt.Equal(currentTime) {
			t.Fatalf("cycle %d: expected checkpoint %v, got %v", i, currentTime, collector.lastCheckedAt)
		}
	}
}

func TestSpendLogCollector_CollectIncremental_FailureDoesNotAdvanceCheckpoint(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")
	db := setupTestDB(t)
	mockClient := &mockSpendLogClient{}

	t0 := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	currentTime := t0
	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }

	if _, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48); err != nil {
		t.Fatalf("initial poll failed: %v", err)
	}

	// Simulate transient gateway failure 5 minutes later: checkpoint must remain t0.
	currentTime = t0.Add(5 * time.Minute)
	mockClient.err = errors.New("gateway timeout")
	if _, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48); err == nil {
		t.Fatalf("expected error on gateway timeout")
	}
	if !collector.lastCheckedAt.Equal(t0) {
		t.Fatalf("checkpoint must not advance on failure: expected %v, got %v", t0, collector.lastCheckedAt)
	}

	// Recovery 15 minutes later: query must resume from t0 - 5m so nothing during outage is skipped.
	currentTime = t0.Add(20 * time.Minute)
	mockClient.err = nil
	res, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("recovery poll failed: %v", err)
	}
	expectedStart := t0.Add(-5 * time.Minute)
	if !res.StartTime.Equal(expectedStart) {
		t.Fatalf("expected recovery start %v, got %v", expectedStart, res.StartTime)
	}
	if !collector.lastCheckedAt.Equal(currentTime) {
		t.Fatalf("expected checkpoint %v after recovery, got %v", currentTime, collector.lastCheckedAt)
	}
}

func TestSpendLogCollector_CollectIncremental_BackwardClockRebasesCheckpoint(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")
	db := setupTestDB(t)
	mockClient := &mockSpendLogClient{}

	currentTime := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }

	if _, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48); err != nil {
		t.Fatalf("initial poll failed: %v", err)
	}

	// Wall clock steps backward to 11:05: must query [11:00, 11:05] and rebase checkpoint to 11:05.
	currentTime = time.Date(2026, 11, 24, 11, 5, 0, 0, time.UTC)
	res1, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("backward-clock poll failed: %v", err)
	}
	wantStart1 := time.Date(2026, 11, 24, 11, 0, 0, 0, time.UTC)
	if !res1.StartTime.Equal(wantStart1) || !res1.EndTime.Equal(currentTime) {
		t.Fatalf("expected backward-clock window [%v, %v], got [%v, %v]", wantStart1, currentTime, res1.StartTime, res1.EndTime)
	}
	if !collector.lastCheckedAt.Equal(currentTime) {
		t.Fatalf("expected checkpoint rebased to %v, got %v", currentTime, collector.lastCheckedAt)
	}

	// Subsequent normal poll at 11:10 covers [11:00, 11:10].
	currentTime = time.Date(2026, 11, 24, 11, 10, 0, 0, time.UTC)
	res2, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("follow-up poll failed: %v", err)
	}
	if !res2.StartTime.Equal(wantStart1) || !res2.EndTime.Equal(currentTime) {
		t.Fatalf("expected follow-up window [%v, %v], got [%v, %v]", wantStart1, currentTime, res2.StartTime, res2.EndTime)
	}
}

func TestSpendLogCollector_CollectIncremental_UncappedReplayAfterRestart(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "")
	db := setupTestDB(t)
	mockClient := &mockSpendLogClient{}

	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	// Seed a 60-day-old watermark (well past the deprecated 30-day cap).
	ancientWatermark := now.Add(-60 * 24 * time.Hour)
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "ancient-wm-1",
		TenantID:    "t-ancient",
		RequestTime: ancientWatermark,
	})

	// Fresh collector instance simulates process restart with zero in-memory checkpoint.
	collector := NewSpendLogCollector(db, mockClient, WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return now }

	res, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("unexpected error during uncapped replay: %v", err)
	}

	expectedStart := ancientWatermark.Add(-5 * time.Minute)
	if !res.StartTime.Equal(expectedStart) {
		t.Fatalf("expected uncapped replay start %v, got %v", expectedStart, res.StartTime)
	}
	if mockClient.lastReq.StartDate != expectedStart.Format("2006-01-02 15:04:05") {
		t.Fatalf("expected gateway StartDate %s, got %s", expectedStart.Format("2006-01-02 15:04:05"), mockClient.lastReq.StartDate)
	}
}

func TestSpendLogCollector_CollectIncremental_PartialNewestFirstReplayPreservesLowerBound(t *testing.T) {
	t.Setenv("METERING_TIMEZONE", "UTC")
	db := setupTestDB(t)

	restartTime := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	seedTime := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "seed-oldest",
		TenantID:    "t-replay",
		RequestTime: seedTime,
	})

	// Simulate LiteLLM /spend/logs/v2 newest-first pagination:
	// page 1 commits a Nov 24 record (advancing DB MAX(request_time)), then page 2 fails.
	newestFirstEntries := []gateway.SpendLogEntry{
		{
			SpendID:     "newest-nov24",
			TeamID:      "t-replay",
			Model:       "gpt-4o",
			TotalTokens: 100,
			Spend:       0.01,
			StartTime:   time.Date(2026, 11, 24, 14, 30, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
		},
		{
			SpendID:     "older-nov20",
			TeamID:      "t-replay",
			Model:       "gpt-4o",
			TotalTokens: 200,
			Spend:       0.02,
			StartTime:   time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05"),
		},
	}

	client := &failOnPageClient{
		entries:  newestFirstEntries,
		failPage: 2,
	}

	currentTime := restartTime
	collector := NewSpendLogCollector(db, client, WithBatchSize(1), WithCollectorTimezone(time.UTC))
	collector.nowFunc = func() time.Time { return currentTime }

	// Attempt 1: page 1 inserts newest-nov24, advancing DB MAX(request_time) to Nov 24 14:30,
	// then page 2 fails before older-nov20 is fetched.
	partialRes, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err == nil {
		t.Fatalf("expected error on page 2 failure")
	}
	if partialRes == nil || partialRes.InsertedCount != 1 {
		t.Fatalf("expected partial result with 1 inserted row, got %+v", partialRes)
	}
	if !collector.lastCheckedAt.IsZero() {
		t.Fatalf("checkpoint must remain zero after failed startup replay")
	}

	// Attempt 2: page 2 succeeds; collector must keep the original lower bound (seedTime - 5m)
	// instead of jumping to Nov 24 14:25 from the partially committed MAX(request_time).
	client.failPage = 0
	currentTime = currentTime.Add(5 * time.Minute)
	retryRes, err := collector.CollectIncremental(context.Background(), 5*time.Minute, 48)
	if err != nil {
		t.Fatalf("retry CollectIncremental failed: %v", err)
	}
	expectedStart := seedTime.Add(-5 * time.Minute)
	if !retryRes.StartTime.Equal(expectedStart) {
		t.Fatalf("expected retry to preserve original replay lower bound %v, got %v", expectedStart, retryRes.StartTime)
	}
	if retryRes.InsertedCount != 1 {
		t.Fatalf("expected older-nov20 to be inserted on retry, got %d", retryRes.InsertedCount)
	}
	if !collector.lastCheckedAt.Equal(currentTime) {
		t.Fatalf("expected checkpoint %v after replay completed, got %v", currentTime, collector.lastCheckedAt)
	}

	// Verify older-nov20 actually landed in DB.
	var count int64
	db.Model(&entity.GatewaySpendLog{}).Where("spend_id = ?", "older-nov20").Count(&count)
	if count != 1 {
		t.Fatalf("expected older-nov20 in DB, got count=%d", count)
	}
}
