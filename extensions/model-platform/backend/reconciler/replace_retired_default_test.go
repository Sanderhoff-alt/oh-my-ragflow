package reconciler

import (
	"context"
	"testing"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

const (
	replacedTenant = "tenant-replaced"
	retiredRef     = "retired-model@platform@OpenAI-API-Compatible"
	fallbackRef    = "fallback-model@platform@OpenAI-API-Compatible"
)

// seedRetiredDefaultUsers creates three users: one on the retired model by the platform, one who chose it
// explicitly, and one on another model.
func seedRetiredDefaultUsers(t *testing.T) (*gorm.DB, *StickyGuard) {
	t.Helper()
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()
	chat := int(rentity.ModelTypeChat)
	createBareTestTenant(db, replacedTenant)

	retiredID := "tm-retired"
	db.Create(&rentity.UserTenant{ID: "ut-platform", UserID: "platform-user", TenantID: replacedTenant, Role: "normal", LLMID: retiredRef, TenantLLMID: &retiredID})
	db.Create(&rentity.UserTenant{ID: "ut-explicit", UserID: "explicit-user", TenantID: replacedTenant, Role: "normal", LLMID: retiredRef, TenantLLMID: &retiredID})
	db.Create(&rentity.UserTenant{ID: "ut-other", UserID: "other-user", TenantID: replacedTenant, Role: "normal", LLMID: "another@custom@P"})
	if err := guard.RecordPlatformDefault(ctx, db, replacedTenant, "platform-user", chat, retiredRef); err != nil {
		t.Fatalf("RecordPlatformDefault failed: %v", err)
	}
	if _, err := guard.RecordObservedUserChoice(ctx, db, replacedTenant, "explicit-user", chat, retiredRef, retiredID); err != nil {
		t.Fatalf("RecordObservedUserChoice failed: %v", err)
	}
	return db, guard
}

func userRow(db *gorm.DB, userID string) rentity.UserTenant {
	var ut rentity.UserTenant
	db.Where("user_id = ?", userID).First(&ut)
	return ut
}

func TestReplaceRetiredDefault_MovesEveryUserOfTheRetiredModelToThePlatformDefault(t *testing.T) {
	db, guard := seedRetiredDefaultUsers(t)
	chat := int(rentity.ModelTypeChat)

	moved, err := guard.ReplaceRetiredDefault(context.Background(), db, replacedTenant, chat, retiredRef, "tm-retired", fallbackRef, "tm-fallback")
	if err != nil || moved != 2 {
		t.Fatalf("expected the two users of the retired model to move, got moved=%d err=%v", moved, err)
	}

	for _, id := range []string{"platform-user", "explicit-user"} {
		ut := userRow(db, id)
		if ut.LLMID != fallbackRef || ut.TenantLLMID == nil || *ut.TenantLLMID != "tm-fallback" {
			t.Errorf("%s: expected the replacement default, got %+v", id, ut)
		}
		var pref mpentity.UserDefaultPreference
		db.Where("user_id = ?", id).First(&pref)
		if pref.Origin != mpentity.OriginPlatformDefault || pref.CurrentModelID != fallbackRef {
			t.Errorf("%s: a moved user must follow the platform default from now on, got %+v", id, pref)
		}
	}
	if other := userRow(db, "other-user"); other.LLMID != "another@custom@P" {
		t.Errorf("a user on another model must be left alone, got %q", other.LLMID)
	}
}

func TestReplaceRetiredDefault_ClearsTheDefaultWhenThereIsNoReplacement(t *testing.T) {
	db, guard := seedRetiredDefaultUsers(t)
	chat := int(rentity.ModelTypeChat)

	moved, err := guard.ReplaceRetiredDefault(context.Background(), db, replacedTenant, chat, retiredRef, "tm-retired", "", "")
	if err != nil || moved != 2 {
		t.Fatalf("expected two users to be cleared, got moved=%d err=%v", moved, err)
	}

	ut := userRow(db, "explicit-user")
	if ut.LLMID != "" || ut.TenantLLMID != nil {
		t.Errorf("expected an empty default instead of a model nobody can use, got %+v", ut)
	}
	var prefs int64
	db.Model(&mpentity.UserDefaultPreference{}).Where("user_id IN ?", []string{"platform-user", "explicit-user"}).Count(&prefs)
	if prefs != 0 {
		t.Errorf("the preferences of cleared users must be dropped so the platform default can fill the slot, got %d", prefs)
	}
}

func TestReplaceRetiredDefault_MatchesByTenantModelIDAndIsIdempotent(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	createBareTestTenant(db, replacedTenant)
	id := "tm-retired"
	// The reference is a bare name, only the tenant model id identifies the retired row.
	db.Create(&rentity.UserTenant{ID: "ut-1", UserID: "user-1", TenantID: replacedTenant, Role: "normal", LLMID: "retired-model", TenantLLMID: &id})

	for i := 0; i < 2; i++ {
		moved, err := guard.ReplaceRetiredDefault(context.Background(), db, replacedTenant, int(rentity.ModelTypeChat), retiredRef, id, fallbackRef, "tm-fallback")
		wantMoved := int64(1 - i)
		if err != nil || moved != wantMoved {
			t.Fatalf("run %d: expected %d moved users, got moved=%d err=%v", i+1, wantMoved, moved, err)
		}
	}
	if ut := userRow(db, "user-1"); ut.LLMID != fallbackRef {
		t.Errorf("expected the replacement default, got %q", ut.LLMID)
	}
}
