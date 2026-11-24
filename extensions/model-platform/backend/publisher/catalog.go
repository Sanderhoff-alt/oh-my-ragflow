package publisher

import (
	"context"
	"sort"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

// TenantStateCounts counts, per model, the tenants in each derived state.
type TenantStateCounts struct {
	Active     int64 `json:"active"`
	Deprecated int64 `json:"deprecated"`
	Tombstoned int64 `json:"tombstoned"`
}

// CatalogEntry is a catalog model with its derived rollout state and reference counts.
type CatalogEntry struct {
	ID                string            `json:"id"`
	ModelName         string            `json:"model_name"`
	ModelType         int               `json:"model_type"`
	IsDefault         bool              `json:"is_default"`
	Status            string            `json:"status"`
	Tenants           TenantStateCounts `json:"tenants"`
	ActiveKBCount     int64             `json:"active_kb_count"`
	ActiveDialogCount int64             `json:"active_dialog_count"`
	CreateTime        int64             `json:"create_time"`
}

// ListCatalog returns every catalog model with the number of tenants in each derived state and the
// number of knowledge bases and assistants that reference it. Tenant states are read from the managed
// platform instances only, so a tenant's own models never count.
func ListCatalog(ctx context.Context, db *gorm.DB) ([]CatalogEntry, error) {
	db = db.WithContext(ctx)

	var models []entity.PlatformManagedModel
	if err := db.Order("model_name ASC").Find(&models).Error; err != nil {
		return nil, err
	}

	var states []struct {
		ModelName  string
		Active     int64
		Deprecated int64
		Tombstoned int64
	}
	if err := db.Raw(`
SELECT m.model_name AS model_name,
	SUM(CASE WHEN m.status = 'active' AND COALESCE(m.extra, '') NOT LIKE '%"deprecated":true%' THEN 1 ELSE 0 END) AS active,
	SUM(CASE WHEN m.status = 'active' AND COALESCE(m.extra, '') LIKE '%"deprecated":true%' THEN 1 ELSE 0 END) AS deprecated,
	SUM(CASE WHEN m.status = 'disabled' AND COALESCE(m.extra, '') LIKE '%"tombstoned":true%' THEN 1 ELSE 0 END) AS tombstoned
FROM tenant_model m
JOIN tenant_model_instance i ON i.id = m.instance_id AND i.instance_name = ?
JOIN tenant_model_provider p ON p.id = i.provider_id AND p.provider_name = ?
GROUP BY m.model_name`,
		reconciler.PlatformInstanceName, reconciler.ProviderOpenAIAPICompatible).Scan(&states).Error; err != nil {
		return nil, err
	}
	stateByName := make(map[string]TenantStateCounts, len(states))
	for _, s := range states {
		stateByName[s.ModelName] = TenantStateCounts{Active: s.Active, Deprecated: s.Deprecated, Tombstoned: s.Tombstoned}
	}

	entries := make([]CatalogEntry, 0, len(models))
	for _, m := range models {
		entry := CatalogEntry{
			ID:         m.ID,
			ModelName:  m.ModelName,
			ModelType:  m.ModelType,
			IsDefault:  m.IsDefault,
			Status:     m.Status,
			Tenants:    stateByName[m.ModelName],
			CreateTime: m.CreateTime,
		}
		if err := countReferences(db, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ModelName < entries[j].ModelName })
	return entries, nil
}

// countReferences counts live knowledge bases (embedding) and assistants (chat) bound to the model, matching
// the bare name, the composite managed reference, or the id of a managed tenant_model row.
func countReferences(db *gorm.DB, entry *CatalogEntry) error {
	composite := entry.ModelName + "@" + reconciler.PlatformInstanceName + "@" + reconciler.ProviderOpenAIAPICompatible
	managedIDs := `SELECT m.id FROM tenant_model m
JOIN tenant_model_instance i ON i.id = m.instance_id AND i.instance_name = ?
WHERE m.model_name = ?`

	switch entry.ModelType {
	case int(rentity.ModelTypeEmbedding):
		return db.Raw(`SELECT COUNT(*) FROM knowledgebase
WHERE (status IS NULL OR status != '0')
AND (embd_id IN (?, ?) OR tenant_embd_id IN (`+managedIDs+`))`,
			entry.ModelName, composite, reconciler.PlatformInstanceName, entry.ModelName).Scan(&entry.ActiveKBCount).Error
	case int(rentity.ModelTypeChat):
		return db.Raw(`SELECT COUNT(*) FROM dialog
WHERE (status IS NULL OR status != '0')
AND (llm_id IN (?, ?) OR tenant_llm_id IN (`+managedIDs+`))`,
			entry.ModelName, composite, reconciler.PlatformInstanceName, entry.ModelName).Scan(&entry.ActiveDialogCount).Error
	}
	return nil
}
