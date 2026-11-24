package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// rotationGateway is a stateful gateway: it tracks which keys exist and which team owns them.
type rotationGateway struct {
	mockAdminGatewayClient
	mu          sync.Mutex
	keys        map[string]string // raw key -> team id
	issued      int
	generateErr error
	deleteErr   error
}

func (g *rotationGateway) GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.generateErr != nil {
		return nil, g.generateErr
	}
	g.issued++
	key := fmt.Sprintf("sk-issued-%d", g.issued)
	g.keys[key] = req.TeamID
	return &gateway.GenerateKeyResponse{Key: key, KeyName: req.KeyName, Models: req.Models}, nil
}

func (g *rotationGateway) ListTeamKeys(ctx context.Context, teamID string) ([]gateway.TeamKey, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []gateway.TeamKey
	for key, team := range g.keys {
		if team == teamID {
			out = append(out, gateway.TeamKey{Token: gateway.HashKey(key), TeamID: team})
		}
	}
	return out, nil
}

func (g *rotationGateway) DeleteKeysByHash(ctx context.Context, hashes []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.deleteErr != nil {
		return g.deleteErr
	}
	for _, h := range hashes {
		for key := range g.keys {
			if gateway.HashKey(key) == h {
				delete(g.keys, key)
			}
		}
	}
	return nil
}

const rotationTenant = "tenant-admin-rotate"

func setupRotationServer(t *testing.T, operator string) (*gin.Engine, *gorm.DB, *rotationGateway) {
	t.Helper()
	t.Setenv("LITELLM_BASE_URL", "")
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:admin_rotation_test_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := migration.MigrateReconcileSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}
	if err := db.AutoMigrate(&rentity.TenantModelProvider{}, &rentity.TenantModelInstance{}, &rentity.TenantModel{}); err != nil {
		t.Fatalf("failed to migrate tenant model tables: %v", err)
	}

	gw := &rotationGateway{keys: map[string]string{"sk-original": rotationTenant}}
	db.Create(&rentity.TenantModelProvider{ID: "p1", TenantID: rotationTenant, ProviderName: "OpenAI-API-Compatible"})
	db.Create(&rentity.TenantModelInstance{ID: "i1", InstanceName: "platform", ProviderID: "p1", APIKey: "sk-original", Status: "active"})
	db.Create(&entity.TenantReconcileRecord{
		TenantID: rotationTenant, Status: entity.StatusReady, KeyHash: gateway.HashKey("sk-original"), Version: 1,
	})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	group := r.Group("/admin")
	if operator != "" {
		group.Use(func(c *gin.Context) { c.Set("user_id", operator) })
	}
	RegisterModelPlatformAdminRoutesWithGateway(group, gw, db)
	return r, db, gw
}

func rotateKey(r *gin.Engine, tenantID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/model-platform/tenants/"+tenantID+"/rotate-key", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

type rotationEnvelope struct {
	Code    int                 `json:"code"`
	Message string              `json:"message"`
	Data    keyRotationResponse `json:"data"`
}

func decodeRotation(t *testing.T, w *httptest.ResponseRecorder) rotationEnvelope {
	t.Helper()
	var out rotationEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid response body %q: %v", w.Body.String(), err)
	}
	return out
}

func TestRotateTenantKeyHandler_Success(t *testing.T) {
	r, db, gw := setupRotationServer(t, "admin-7")

	w := rotateKey(r, rotationTenant)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	data := decodeRotation(t, w).Data
	if !data.OldKeysRevoked || data.RevokedKeys != 1 || data.TenantID != rotationTenant || len(data.NewKeyFingerprint) != 8 {
		t.Errorf("unexpected rotation data: %+v", data)
	}
	if bodyHasKey(w.Body.String()) {
		t.Errorf("the response must not carry a raw key: %s", w.Body.String())
	}

	if _, ok := gw.keys["sk-original"]; ok || len(gw.keys) != 1 {
		t.Errorf("the original key must be revoked and exactly one key remain, got %v", gw.keys)
	}
	var instance rentity.TenantModelInstance
	db.Where("id = ?", "i1").First(&instance)
	if instance.APIKey == "sk-original" || instance.APIKey == "" {
		t.Errorf("the instance must use the new key, got %q", instance.APIKey)
	}
	var rec entity.TenantReconcileRecord
	db.Where("tenant_id = ?", rotationTenant).First(&rec)
	if rec.KeyRotatedBy != "admin-7" || rec.KeyHash != gateway.HashKey(instance.APIKey) {
		t.Errorf("the record must name the new key and the operator: %+v", rec)
	}
}

func bodyHasKey(body string) bool {
	return strings.Contains(body, "sk-issued") || strings.Contains(body, "sk-original")
}

func TestRotateTenantKeyHandler_PendingRevocationIsAccepted(t *testing.T) {
	r, _, gw := setupRotationServer(t, "admin-7")
	gw.deleteErr = errors.New("gateway returned error status 500")

	w := rotateKey(r, rotationTenant)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if decodeRotation(t, w).Data.OldKeysRevoked {
		t.Error("the response must say that the old key may still be valid")
	}
}
