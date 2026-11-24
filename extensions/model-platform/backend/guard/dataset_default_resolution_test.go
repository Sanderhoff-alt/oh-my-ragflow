package guard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	retiredRef = "bge-m3@platform@OpenAI-API-Compatible"
	healthyRef = "bge-large@platform@OpenAI-API-Compatible"
)

// seedEmbeddingRows gives the tenant a tombstoned managed bge-m3, a healthy managed bge-large, and its own
// healthy instance that also has a model named bge-m3.
func seedEmbeddingRows(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.AutoMigrate(&rentity.TenantModelInstance{}); err != nil {
		t.Fatalf("failed to migrate instances: %v", err)
	}
	rows := []interface{}{
		&rentity.TenantModelProvider{ID: "prov", TenantID: "tenant-1", ProviderName: "OpenAI-API-Compatible"},
		&rentity.TenantModelInstance{ID: "inst-platform", InstanceName: "platform", ProviderID: "prov", APIKey: "sk", Status: "active"},
		&rentity.TenantModelInstance{ID: "inst-mine", InstanceName: "mine", ProviderID: "prov", APIKey: "sk2", Status: "active"},
		&rentity.TenantModel{ID: "tm-retired", ModelName: "bge-m3", ProviderID: "prov", InstanceID: "inst-platform", ModelType: 2, Status: "disabled", Extra: `{"is_managed":true,"tombstoned":true}`},
		&rentity.TenantModel{ID: "tm-healthy", ModelName: "bge-large", ProviderID: "prov", InstanceID: "inst-platform", ModelType: 2, Status: "active", Extra: `{"is_managed":true}`},
		&rentity.TenantModel{ID: "tm-own", ModelName: "bge-m3", ProviderID: "prov", InstanceID: "inst-mine", ModelType: 2, Status: "active", Extra: `{}`},
	}
	for _, row := range rows {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("failed to seed: %v", err)
		}
	}
}

func TestIsModelDeprecated_ScopesReferencesToTheirInstance(t *testing.T) {
	db := setupGuardTestDB(t)
	seedEmbeddingRows(t, db)
	ctx := context.Background()

	cases := []struct {
		ref  string
		want bool
	}{
		{retiredRef, true},
		{healthyRef, false},
		{"bge-m3@mine@OpenAI-API-Compatible", false},
		{"bge-m3", false},
		{"tm-retired", true},
	}
	for _, tc := range cases {
		if got := isModelDeprecated(ctx, db, "tenant-1", tc.ref); got != tc.want {
			t.Errorf("isModelDeprecated(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
	if isModelDeprecated(ctx, db, "tenant-1", "never-heard-of") {
		t.Error("an unknown model is left to the upstream validation")
	}
}

func TestDatasetCreate_UsesTheRequestingUsersDefaultEmbedding(t *testing.T) {
	db := setupGuardTestDB(t)
	seedEmbeddingRows(t, db)
	oldest, newest := int64(1000), int64(2000)
	db.Create(&rentity.UserTenant{ID: "ut-old", UserID: "old-user", TenantID: "tenant-1", Role: "owner", EmbdID: retiredRef, BaseModel: rentity.BaseModel{CreateTime: &oldest}})
	db.Create(&rentity.UserTenant{ID: "ut-new", UserID: "new-user", TenantID: "tenant-1", Role: "normal", EmbdID: healthyRef, BaseModel: rentity.BaseModel{CreateTime: &newest}})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("tenant_id", "tenant-1")
		c.Set("user_id", c.GetHeader("X-User-ID"))
		c.Next()
	})
	r.Use(DatasetImmutabilityGuardMiddleware(db))
	r.POST("/api/v1/datasets", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "created"}) })

	create := func(userID string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/datasets", bytes.NewBufferString(`{"name":"kb"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	if code := create("new-user"); code != http.StatusOK {
		t.Errorf("a user whose default embedding is healthy must not be blocked by another member's default, got %d", code)
	}
	if code := create("old-user"); code != http.StatusBadRequest {
		t.Errorf("a user whose own default embedding is retired must be blocked, got %d", code)
	}
}
