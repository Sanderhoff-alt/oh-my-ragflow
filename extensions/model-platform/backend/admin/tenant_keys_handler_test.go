package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
)

type tenantListEnvelope struct {
	Data struct {
		Total    int64           `json:"total"`
		Page     int             `json:"page"`
		PageSize int             `json:"page_size"`
		Items    []tenantKeyItem `json:"items"`
	} `json:"data"`
}

func listTenants(t *testing.T, query string) (int, tenantListEnvelope, string) {
	t.Helper()
	r, db, _ := setupRotationServer(t, "admin-7")
	rotated := time.Now()
	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-b", Status: entity.StatusReady, KeyHash: "0123456789abcdef", KeyRotatedBy: "admin-1", KeyRotatedAt: &rotated, Version: 1})
	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-c", Status: entity.StatusDegraded, Version: 1})
	for i := 0; i < 3; i++ {
		db.Create(&entity.TenantReconcileRecord{TenantID: fmt.Sprintf("tenant-z%d", i), Status: entity.StatusReady, Version: 1})
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/model-platform/tenants"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out tenantListEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestListTenantKeysHandler_ListsReadyTenantsByDefault(t *testing.T) {
	code, out, body := listTenants(t, "")
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", code, body)
	}
	if out.Data.Total != 5 || len(out.Data.Items) != 5 {
		t.Fatalf("expected the 5 ready tenants, got total=%d items=%d", out.Data.Total, len(out.Data.Items))
	}
	if out.Data.Items[0].TenantID != rotationTenant || out.Data.Items[1].TenantID != "tenant-b" {
		t.Errorf("tenants must be ordered by id, got %+v", out.Data.Items[:2])
	}
	b := out.Data.Items[1]
	if b.KeyFingerprint != "01234567" || b.KeyRotatedBy != "admin-1" || b.KeyRotatedAt == nil {
		t.Errorf("unexpected key state: %+v", b)
	}
	if bodyHasKey(body) || len(out.Data.Items[0].KeyFingerprint) > 8 {
		t.Errorf("the list must not expose key material: %s", body)
	}
}

func TestListTenantKeysHandler_FiltersAndPaginates(t *testing.T) {
	_, out, _ := listTenants(t, "?status=degraded")
	if out.Data.Total != 1 || out.Data.Items[0].TenantID != "tenant-c" {
		t.Errorf("expected only the degraded tenant, got %+v", out.Data)
	}

	_, out, _ = listTenants(t, "?page=2&page_size=2")
	if out.Data.Total != 5 || len(out.Data.Items) != 2 || out.Data.Page != 2 || out.Data.PageSize != 2 {
		t.Errorf("expected the second page of two items, got %+v", out.Data)
	}

	_, out, _ = listTenants(t, "?page_size=100000")
	if out.Data.PageSize != maxTenantPageSize {
		t.Errorf("page size must be capped at %d, got %d", maxTenantPageSize, out.Data.PageSize)
	}
}
