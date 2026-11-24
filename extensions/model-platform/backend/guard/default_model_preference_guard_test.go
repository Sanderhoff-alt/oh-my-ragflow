package guard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	prefTenantID = "tenant-pref"
	platformChat = "gpt-4o-mini@platform@OpenAI-API-Compatible"
	userChat     = "deepseek-chat@custom@OpenAI-API-Compatible"
	upgradedChat = "claude-3-5-sonnet@platform@OpenAI-API-Compatible"
)

func setupPreferenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbName := fmt.Sprintf("file:guard_pref_test_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dbName), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&rentity.UserTenant{}, &mpentity.UserDefaultPreference{}); err != nil {
		t.Fatalf("failed to auto migrate: %v", err)
	}
	return db
}

func seedPlatformDefaultUser(t *testing.T, db *gorm.DB, userID string) {
	t.Helper()
	ut := rentity.UserTenant{ID: "ut-" + userID, UserID: userID, TenantID: prefTenantID, Role: "normal", LLMID: platformChat}
	if err := db.Create(&ut).Error; err != nil {
		t.Fatalf("failed to seed user_tenant: %v", err)
	}
	if err := reconciler.NewStickyGuard().RecordPlatformDefault(
		context.Background(), db, prefTenantID, userID, int(rentity.ModelTypeChat), platformChat,
	); err != nil {
		t.Fatalf("failed to record platform default: %v", err)
	}
}

// setupPreferenceServer mounts a fake upstream handler that applies mutate to the caller's user_tenant row.
func setupPreferenceServer(db *gorm.DB, mutate func(db *gorm.DB, userID string)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("tenant_id", prefTenantID)
		if uid := c.GetHeader("X-User-ID"); uid != "" {
			c.Set("user_id", uid)
		}
		c.Next()
	})
	r.Use(DefaultModelPreferenceMiddleware(db))

	handler := func(c *gin.Context) {
		mutate(db, c.GetString("user_id"))
		c.JSON(http.StatusOK, gin.H{"code": 0})
	}
	r.PATCH("/api/v1/models/default", handler)
	r.PATCH("/api/v1/models", handler)
	r.GET("/api/v1/models/default", handler)
	r.PATCH("/api/v1/models/groups", handler)
	return r
}

func doRequest(r *gin.Engine, method, path, userID string) {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
}

func setChat(model string) func(db *gorm.DB, userID string) {
	return func(db *gorm.DB, userID string) {
		db.Model(&rentity.UserTenant{}).Where("user_id = ?", userID).Update("llm_id", model)
	}
}

func loadPref(t *testing.T, db *gorm.DB, userID string, modelType rentity.ModelType) (mpentity.UserDefaultPreference, bool) {
	t.Helper()
	var pref mpentity.UserDefaultPreference
	res := db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", prefTenantID, userID, int(modelType)).Limit(1).Find(&pref)
	return pref, res.RowsAffected > 0
}

func TestDefaultModelPreference_ChangeRecordedAndPreservedOnUpgrade(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	seedPlatformDefaultUser(t, db, "bob")
	r := setupPreferenceServer(db, setChat(userChat))

	doRequest(r, http.MethodPatch, "/api/v1/models/default", "alice")

	pref, ok := loadPref(t, db, "alice", rentity.ModelTypeChat)
	if !ok || pref.Origin != mpentity.OriginUserExplicit || pref.CurrentModelID != userChat {
		t.Fatalf("expected user_explicit pref for %s, got ok=%v pref=%+v", userChat, ok, pref)
	}

	res, err := reconciler.NewStickyGuard().UpgradeDefaultModel(
		context.Background(), db, prefTenantID, int(rentity.ModelTypeChat), upgradedChat, "",
	)
	if err != nil {
		t.Fatalf("UpgradeDefaultModel failed: %v", err)
	}
	if res.PreservedCount != 1 || res.UpgradedCount != 1 {
		t.Fatalf("expected 1 preserved and 1 upgraded, got %+v", res)
	}

	var alice, bobRow rentity.UserTenant
	db.Where("user_id = ?", "alice").First(&alice)
	db.Where("user_id = ?", "bob").First(&bobRow)
	if alice.LLMID != userChat {
		t.Errorf("alice's explicit choice was overwritten: %s", alice.LLMID)
	}
	if bobRow.LLMID != upgradedChat {
		t.Errorf("bob never changed his default and should be upgraded, got %s", bobRow.LLMID)
	}
}

func TestDefaultModelPreference_FailedUpdateRecordsNothing(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	r := setupPreferenceServer(db, func(*gorm.DB, string) {})

	doRequest(r, http.MethodPatch, "/api/v1/models/default", "alice")

	pref, _ := loadPref(t, db, "alice", rentity.ModelTypeChat)
	if pref.Origin != mpentity.OriginPlatformDefault {
		t.Errorf("expected origin to stay platform_default, got %s", pref.Origin)
	}
}

func TestDefaultModelPreference_ReselectingSameModelRecordsNothing(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	r := setupPreferenceServer(db, setChat(platformChat))

	doRequest(r, http.MethodPatch, "/api/v1/models/default", "alice")

	pref, _ := loadPref(t, db, "alice", rentity.ModelTypeChat)
	if pref.Origin != mpentity.OriginPlatformDefault {
		t.Errorf("expected origin to stay platform_default, got %s", pref.Origin)
	}
}

func TestDefaultModelPreference_IgnoresUnrelatedRoutes(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	r := setupPreferenceServer(db, setChat(userChat))

	doRequest(r, http.MethodGet, "/api/v1/models/default", "alice")
	doRequest(r, http.MethodPatch, "/api/v1/models/groups", "alice")

	pref, _ := loadPref(t, db, "alice", rentity.ModelTypeChat)
	if pref.Origin != mpentity.OriginPlatformDefault {
		t.Errorf("unrelated routes must not record a preference, got %s", pref.Origin)
	}
}

func TestDefaultModelPreference_MissingUserPassesThrough(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	r := setupPreferenceServer(db, setChat(userChat))

	doRequest(r, http.MethodPatch, "/api/v1/models/default", "")

	pref, _ := loadPref(t, db, "alice", rentity.ModelTypeChat)
	if pref.Origin != mpentity.OriginPlatformDefault {
		t.Errorf("expected no preference change without an authenticated user, got %s", pref.Origin)
	}
}

func TestDefaultModelPreference_MultiTypeUpdateRecordsEachChangedType(t *testing.T) {
	db := setupPreferenceTestDB(t)
	seedPlatformDefaultUser(t, db, "alice")
	const newEmbd = "bge-m3@custom@OpenAI-API-Compatible"
	r := setupPreferenceServer(db, func(db *gorm.DB, userID string) {
		db.Model(&rentity.UserTenant{}).Where("user_id = ?", userID).
			Updates(map[string]interface{}{"llm_id": userChat, "embd_id": newEmbd})
	})

	doRequest(r, http.MethodPatch, "/api/v1/models", "alice")

	for modelType, want := range map[rentity.ModelType]string{
		rentity.ModelTypeChat:      userChat,
		rentity.ModelTypeEmbedding: newEmbd,
	} {
		pref, ok := loadPref(t, db, "alice", modelType)
		if !ok || pref.Origin != mpentity.OriginUserExplicit || pref.CurrentModelID != want {
			t.Errorf("model type %d: expected user_explicit %s, got ok=%v pref=%+v", modelType, want, ok, pref)
		}
	}
	if _, ok := loadPref(t, db, "alice", rentity.ModelTypeRerank); ok {
		t.Error("unchanged rerank default must not get a preference row")
	}
}
