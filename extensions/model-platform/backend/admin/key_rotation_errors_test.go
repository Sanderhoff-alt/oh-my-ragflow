package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	rentity "ragflow/internal/entity"
)

func TestRotateTenantKeyHandler_GatewayFailureKeepsTheCurrentKey(t *testing.T) {
	r, db, gw := setupRotationServer(t, "admin-7")
	gw.generateErr = errors.New("gateway unreachable")

	w := rotateKey(r, rotationTenant)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", w.Code, w.Body.String())
	}
	if _, ok := gw.keys["sk-original"]; !ok {
		t.Error("the current key must stay valid")
	}
	var instance rentity.TenantModelInstance
	db.Where("id = ?", "i1").First(&instance)
	if instance.APIKey != "sk-original" {
		t.Errorf("the instance must keep its key, got %q", instance.APIKey)
	}
}

func TestRotateTenantKeyHandler_RejectsRequestsThatCannotRotate(t *testing.T) {
	r, db, _ := setupRotationServer(t, "admin-7")
	db.Create(&entity.TenantReconcileRecord{TenantID: "tenant-pending", Status: entity.StatusPending, Version: 1})

	cases := []struct {
		name     string
		tenantID string
		want     int
	}{
		{"unknown tenant", "tenant-unknown", http.StatusNotFound},
		{"tenant without a managed instance", "tenant-pending", http.StatusConflict},
		{"tenant id that cannot be a team id", "bad$id", http.StatusBadRequest},
	}
	for _, tc := range cases {
		if w := rotateKey(r, tc.tenantID); w.Code != tc.want {
			t.Errorf("%s: expected %d, got %d: %s", tc.name, tc.want, w.Code, w.Body.String())
		}
	}
}

func TestRotateTenantKeyHandler_RequiresAnOperator(t *testing.T) {
	r, _, gw := setupRotationServer(t, "")

	w := rotateKey(r, rotationTenant)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
	if len(gw.keys) != 1 || gw.issued != 0 {
		t.Error("an unattributable request must not touch the gateway")
	}
}

func TestRotateTenantKeyHandler_UnavailableWithoutGateway(t *testing.T) {
	r, _ := setupPublishAdminServer(t)

	w := rotateKey(r, rotationTenant)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRotateTenantKeyHandler_RotatingTwiceStaysOnOneKey(t *testing.T) {
	r, _, gw := setupRotationServer(t, "admin-7")

	for i := 0; i < 2; i++ {
		if w := rotateKey(r, rotationTenant); w.Code != http.StatusOK {
			t.Fatalf("rotation %d: expected 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}
	keys, _ := gw.ListTeamKeys(context.Background(), rotationTenant)
	if len(keys) != 1 || keys[0].Token == gateway.HashKey("sk-original") {
		t.Errorf("expected a single replacement key, got %+v", keys)
	}
}
