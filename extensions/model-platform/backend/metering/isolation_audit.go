package metering

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"ragflow/extensions/model-platform/backend/entity"

	"gorm.io/gorm"
)

// AuditIsolationReport details the findings of a tenant spend isolation audit.
type AuditIsolationReport struct {
	TenantID                string   `json:"tenant_id"`
	ManagedSpendTotal       float64  `json:"managed_spend_total"`
	SelfOwnedSpendTotal     float64  `json:"self_owned_spend_total"`
	UnmanagedModelsDetected []string `json:"unmanaged_models_detected,omitempty"`
	AuditPassed             bool     `json:"audit_passed"`
	Violations              []string `json:"violations,omitempty"`
}

// AuditTenantSpendIsolation performs an automated audit on a tenant's recorded gateway spend logs,
// verifying from the Single Source of Truth (platform_gateway_spend_log) that:
// 1. Only authorized managed models generate platform spend.
// 2. Tenant self-owned models generate exactly 0.000000 platform cost.
// 3. No unmanaged or custom models appear in platform gateway spend logs.
//
// Model comparison uses exact string matching to prevent security masquerading and to expose upstream data anomalies.
// If managedModelsOverride is omitted or empty, the authorized models are automatically resolved from the database.
func AuditTenantSpendIsolation(ctx context.Context, db *gorm.DB, tenantID string, managedModelsOverride ...[]string) (*AuditIsolationReport, error) {
	if db == nil {
		return nil, errors.New("database handle cannot be nil")
	}
	if strings.TrimSpace(tenantID) == "" {
		return nil, errors.New("tenantID cannot be empty")
	}

	report := &AuditIsolationReport{
		TenantID:    tenantID,
		AuditPassed: true,
	}

	var managedModels []string
	if len(managedModelsOverride) > 0 && len(managedModelsOverride[0]) > 0 {
		managedModels = managedModelsOverride[0]
	} else {
		// Automatically resolve tenant's managed platform models from database
		err := db.WithContext(ctx).
			Table("tenant_model").
			Select("tenant_model.model_name").
			Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model.provider_id").
			Joins("JOIN tenant_model_instance ON tenant_model_instance.id = tenant_model.instance_id").
			Where("tenant_model_provider.tenant_id = ? AND tenant_model_provider.provider_name = ? AND tenant_model_instance.instance_name = ?",
				tenantID, "OpenAI-API-Compatible", "platform").
			Pluck("tenant_model.model_name", &managedModels).Error
		if err != nil {
			return nil, fmt.Errorf("failed to resolve managed models for tenant %s: %w", tenantID, err)
		}
	}

	managedSet := make(map[string]bool)
	for _, m := range managedModels {
		managedSet[m] = true
	}

	// Audit platform_gateway_spend_log for tenant (Single Source of Truth)
	// Stream rows incrementally with projection to avoid holding large log histories in memory.
	rows, err := db.WithContext(ctx).
		Model(&entity.GatewaySpendLog{}).
		Select("model_name, spend").
		Where("tenant_id = ?", tenantID).
		Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to query spend logs for audit: %w", err)
	}
	defer rows.Close()

	unmanagedRawModels := make(map[string]float64)
	for rows.Next() {
		var modelName string
		var spend float64
		if err := rows.Scan(&modelName, &spend); err != nil {
			return nil, fmt.Errorf("failed to scan spend log row: %w", err)
		}
		if managedSet[modelName] {
			report.ManagedSpendTotal += spend
		} else {
			unmanagedRawModels[modelName] += spend
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error during spend logs iteration: %w", err)
	}

	if len(unmanagedRawModels) > 0 {
		report.AuditPassed = false
		for m, spend := range unmanagedRawModels {
			report.UnmanagedModelsDetected = append(report.UnmanagedModelsDetected, m)
			report.SelfOwnedSpendTotal += spend
			report.Violations = append(report.Violations,
				fmt.Sprintf("unmanaged model %q found in raw gateway logs: spend=%f", m, spend))
		}
		sort.Strings(report.UnmanagedModelsDetected)
		sort.Strings(report.Violations)
	}

	return report, nil
}
