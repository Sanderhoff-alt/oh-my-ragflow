package metering

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAuditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:audit_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
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

func TestIsolationAudit_TenantSelfOwnedModelZeroBilling(t *testing.T) {
	db := setupAuditTestDB(t)
	tenantID := "tenant-dual-mode"

	managedModels := []string{"gpt-4o", "text-embedding-3-small"}

	// Simulate Tenant using both Platform Managed Model and Self-Owned Model:
	// 1. Self-Owned Model (e.g. BYOK OpenAI key or self-hosted Ollama)
	//    Calls bypass LiteLLM Proxy entirely and hit upstream provider directly.
	//    Therefore, LiteLLM gateway emits 0 spend logs for this model.
	// 2. Platform Managed Model (gpt-4o)
	//    Calls route through LiteLLM Proxy with virtual key, generating a gateway spend log.

	gatewayLogs := []gateway.SpendLogEntry{
		{
			SpendID:          "gw-spend-managed-1",
			Model:            "gpt-4o",
			TeamID:           tenantID,
			CallType:         "chat",
			TotalTokens:      5000,
			PromptTokens:     3500,
			CompletionTokens: 1500,
			Spend:            0.050000,
			StartTime:        "2026-11-15 10:00:00",
		},
	}

	mockClient := &mockSpendLogClient{entries: gatewayLogs}
	collector := NewSpendLogCollector(db, mockClient)

	// Ingest gateway logs
	start := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 15, 23, 59, 59, 0, time.UTC)
	if _, err := collector.CollectSpendLogs(context.Background(), start, end); err != nil {
		t.Fatalf("collector failed: %v", err)
	}

	// Run audit using Single Source of Truth (platform_gateway_spend_log)
	report, err := AuditTenantSpendIsolation(context.Background(), db, tenantID, managedModels)
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}

	if !report.AuditPassed {
		t.Fatalf("expected audit to pass, got violations: %v", report.Violations)
	}
	if math.Abs(report.ManagedSpendTotal-0.05) > 1e-6 {
		t.Errorf("expected managed spend 0.05, got %f", report.ManagedSpendTotal)
	}
	if report.SelfOwnedSpendTotal != 0.000000 {
		t.Errorf("expected self-owned spend exactly 0.000000, got %f", report.SelfOwnedSpendTotal)
	}
	if len(report.UnmanagedModelsDetected) != 0 {
		t.Errorf("expected 0 unmanaged models, got %v", report.UnmanagedModelsDetected)
	}

	// Verify database state: no records exist for any self-owned model
	var unmanagedInDB int64
	db.Model(&entity.GatewaySpendLog{}).
		Where("tenant_id = ? AND model_name NOT IN ?", tenantID, managedModels).
		Count(&unmanagedInDB)
	if unmanagedInDB != 0 {
		t.Fatalf("expected 0 unmanaged logs in gateway spend log table, got %d", unmanagedInDB)
	}
}

func TestIsolationAudit_PureSelfOwnedTenantZeroPlatformFootprint(t *testing.T) {
	db := setupAuditTestDB(t)
	tenantID := "tenant-pure-byok"
	managedModels := []string{"gpt-4o", "text-embedding-3-small"}

	// Pure BYOK tenant generates 0 platform gateway logs
	mockClient := &mockSpendLogClient{entries: []gateway.SpendLogEntry{}}
	collector := NewSpendLogCollector(db, mockClient)

	start := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 15, 23, 59, 59, 0, time.UTC)
	if _, err := collector.CollectSpendLogs(context.Background(), start, end); err != nil {
		t.Fatalf("collector failed: %v", err)
	}

	report, err := AuditTenantSpendIsolation(context.Background(), db, tenantID, managedModels)
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}

	if !report.AuditPassed {
		t.Errorf("audit should pass for pure BYOK tenant, got violations: %v", report.Violations)
	}
	if report.ManagedSpendTotal != 0.000000 || report.SelfOwnedSpendTotal != 0.000000 {
		t.Errorf("pure BYOK tenant must have 0 platform spend, got managed=%f, selfOwned=%f",
			report.ManagedSpendTotal, report.SelfOwnedSpendTotal)
	}
}

func TestIsolationAudit_TamperDetectionAndViolationReporting(t *testing.T) {
	db := setupAuditTestDB(t)
	tenantID := "tenant-compromised"
	managedModels := []string{"gpt-4o", "text-embedding-3-small"}

	// Injected unmanaged model log in platform_gateway_spend_log
	rogueLog := entity.GatewaySpendLog{
		SpendID:          "rogue-spend-999",
		TenantID:         tenantID,
		ModelName:        "unauthorized-custom-model",
		CallType:         "chat",
		PromptTokens:     1000,
		CompletionTokens: 500,
		TotalTokens:      1500,
		Spend:            0.035000,
		RequestTime:      time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
	}
	db.Create(&rogueLog)

	report, err := AuditTenantSpendIsolation(context.Background(), db, tenantID, managedModels)
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}

	if report.AuditPassed {
		t.Fatal("expected audit to FAIL when unmanaged model incurs platform spend, but it passed")
	}
	if len(report.UnmanagedModelsDetected) != 1 || report.UnmanagedModelsDetected[0] != "unauthorized-custom-model" {
		t.Errorf("expected unauthorized-custom-model detected, got %v", report.UnmanagedModelsDetected)
	}
	if math.Abs(report.SelfOwnedSpendTotal-0.035) > 1e-6 {
		t.Errorf("expected self-owned spend 0.035, got %f", report.SelfOwnedSpendTotal)
	}
	if len(report.Violations) == 0 {
		t.Error("expected detailed violation log, got none")
	}
}

func TestIsolationAudit_CrossTenantZeroLeakage(t *testing.T) {
	db := setupAuditTestDB(t)
	managedModels := []string{"gpt-4o", "text-embedding-3-small"}

	// Tenant 1 has heavy platform model spend ($125.00)
	heavyLog := entity.GatewaySpendLog{
		SpendID:     "s-heavy-1",
		TenantID:    "tenant-heavy",
		ModelName:   "gpt-4o",
		TotalTokens: 10000000,
		Spend:       125.000000,
		RequestTime: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
	}
	db.Create(&heavyLog)

	// Tenant 2 has NO platform activity
	report2, err := AuditTenantSpendIsolation(context.Background(), db, "tenant-quiet", managedModels)
	if err != nil {
		t.Fatalf("audit failed for tenant-quiet: %v", err)
	}

	if !report2.AuditPassed {
		t.Errorf("tenant-quiet audit failed: %v", report2.Violations)
	}
	if report2.ManagedSpendTotal != 0.0 || report2.SelfOwnedSpendTotal != 0.0 {
		t.Fatalf("Cross-tenant leakage detected! Quiet tenant incurred spend: managed=%f, selfOwned=%f",
			report2.ManagedSpendTotal, report2.SelfOwnedSpendTotal)
	}
}

func TestIsolationAudit_ExactMatchSecurity_NoSilentMasking(t *testing.T) {
	db := setupAuditTestDB(t)
	tenantID := "tenant-spoofed"
	managedModels := []string{"gpt-4o"}

	// Rogue log attempting to masquerade with surrounding whitespace: " gpt-4o "
	rogueLog := entity.GatewaySpendLog{
		SpendID:     "rogue-space-1",
		TenantID:    tenantID,
		ModelName:   " gpt-4o ",
		CallType:    "chat",
		TotalTokens: 1000,
		Spend:       0.020000,
		RequestTime: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC),
	}
	db.Create(&rogueLog)

	report, err := AuditTenantSpendIsolation(context.Background(), db, tenantID, managedModels)
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}

	// Must NOT be silently normalized into "gpt-4o". It is an unmanaged, anomalous model identifier!
	if report.AuditPassed {
		t.Fatal("expected audit to FAIL for rogue model with spaces ' gpt-4o ', but normalization passed it")
	}
	if len(report.UnmanagedModelsDetected) != 1 || report.UnmanagedModelsDetected[0] != " gpt-4o " {
		t.Errorf("expected ' gpt-4o ' to be flagged as unmanaged, got %v", report.UnmanagedModelsDetected)
	}
	if math.Abs(report.SelfOwnedSpendTotal-0.02) > 1e-6 {
		t.Errorf("expected self-owned spend 0.02, got %f", report.SelfOwnedSpendTotal)
	}
}

func TestIsolationAudit_AutoResolveManagedModelsFromDB(t *testing.T) {
	db := setupAuditTestDB(t)
	tenantID := "tenant-auto-resolve"

	// Create minimal tenant_model relational schema
	db.Exec(`CREATE TABLE IF NOT EXISTS tenant_model_provider (
		id VARCHAR(32) PRIMARY KEY,
		tenant_id VARCHAR(32),
		provider_name VARCHAR(64)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS tenant_model_instance (
		id VARCHAR(32) PRIMARY KEY,
		provider_id VARCHAR(32),
		instance_name VARCHAR(64)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS tenant_model (
		id VARCHAR(32) PRIMARY KEY,
		provider_id VARCHAR(32),
		instance_id VARCHAR(32),
		model_name VARCHAR(128)
	)`)

	// Seed managed platform model: gpt-4o
	db.Exec(`INSERT INTO tenant_model_provider (id, tenant_id, provider_name) VALUES ('p1', ?, 'OpenAI-API-Compatible')`, tenantID)
	db.Exec(`INSERT INTO tenant_model_instance (id, provider_id, instance_name) VALUES ('i1', 'p1', 'platform')`)
	db.Exec(`INSERT INTO tenant_model (id, provider_id, instance_id, model_name) VALUES ('m1', 'p1', 'i1', 'gpt-4o')`)

	// Insert managed spend log
	db.Create(&entity.GatewaySpendLog{
		SpendID:     "s-auto-1",
		TenantID:    tenantID,
		ModelName:   "gpt-4o",
		CallType:    "chat",
		TotalTokens: 2000,
		Spend:       0.050000,
		RequestTime: time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC),
	})

	// Invoke AuditTenantSpendIsolation WITHOUT passing any model list!
	report, err := AuditTenantSpendIsolation(context.Background(), db, tenantID)
	if err != nil {
		t.Fatalf("audit failed: %v", err)
	}

	if !report.AuditPassed {
		t.Fatalf("expected audit to pass, got violations: %v", report.Violations)
	}
	if math.Abs(report.ManagedSpendTotal-0.05) > 1e-6 {
		t.Errorf("expected managed spend 0.05, got %f", report.ManagedSpendTotal)
	}
	if report.SelfOwnedSpendTotal != 0.0 {
		t.Errorf("expected 0 self-owned spend, got %f", report.SelfOwnedSpendTotal)
	}
}
