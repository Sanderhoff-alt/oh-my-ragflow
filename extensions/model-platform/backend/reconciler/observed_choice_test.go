package reconciler

import (
	"context"
	"testing"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"
)

const observedTenant = "tenant-observed"

func TestRecordObservedUserChoice_RecordsTheObservedDefaultWithoutWritingUserTenant(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	createBareTestTenant(db, observedTenant)
	createTestUserTenantInDB(db, "user-1", observedTenant, "deepseek-v3@custom@P", "")
	chat := int(rentity.ModelTypeChat)

	recorded, err := guard.RecordObservedUserChoice(context.Background(), db, observedTenant, "user-1", chat, "deepseek-v3@custom@P", "")
	if err != nil || !recorded {
		t.Fatalf("expected the observed choice to be recorded, got recorded=%v err=%v", recorded, err)
	}

	var pref mpentity.UserDefaultPreference
	db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", observedTenant, "user-1", chat).First(&pref)
	if pref.Origin != mpentity.OriginUserExplicit || pref.CurrentModelID != "deepseek-v3@custom@P" {
		t.Errorf("expected a user_explicit preference, got %+v", pref)
	}
	var ut rentity.UserTenant
	db.Where("user_id = ?", "user-1").First(&ut)
	if ut.LLMID != "deepseek-v3@custom@P" {
		t.Errorf("user_tenant must stay as the upstream handler left it, got %q", ut.LLMID)
	}
}

func TestRecordObservedUserChoice_KeepsALaterDefaultChange(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	createBareTestTenant(db, observedTenant)
	// A second update committed after the middleware observed "first-choice".
	createTestUserTenantInDB(db, "user-1", observedTenant, "second-choice@custom@P", "")

	recorded, err := guard.RecordObservedUserChoice(context.Background(), db, observedTenant, "user-1",
		int(rentity.ModelTypeChat), "first-choice@custom@P", "")
	if err != nil || recorded {
		t.Fatalf("expected nothing to be recorded for a stale observation, got recorded=%v err=%v", recorded, err)
	}

	var ut rentity.UserTenant
	db.Where("user_id = ?", "user-1").First(&ut)
	if ut.LLMID != "second-choice@custom@P" {
		t.Errorf("the later default must not be restored to the older value, got %q", ut.LLMID)
	}
	var prefs int64
	db.Model(&mpentity.UserDefaultPreference{}).Where("user_id = ?", "user-1").Count(&prefs)
	if prefs != 0 {
		t.Errorf("a stale observation must not create a preference, got %d", prefs)
	}
}

func TestRecordObservedUserChoice_IgnoresADefaultThePlatformAssigned(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	createBareTestTenant(db, observedTenant)
	createTestUserTenantInDB(db, "user-1", observedTenant, "upgraded@platform@P", "")
	chat := int(rentity.ModelTypeChat)
	// A platform upgrade committed between the middleware's two reads, so the default changed
	// although the request itself was rejected.
	if err := guard.RecordPlatformDefault(context.Background(), db, observedTenant, "user-1", chat, "upgraded@platform@P"); err != nil {
		t.Fatalf("RecordPlatformDefault failed: %v", err)
	}

	recorded, err := guard.RecordObservedUserChoice(context.Background(), db, observedTenant, "user-1", chat, "upgraded@platform@P", "")
	if err != nil || recorded {
		t.Fatalf("a platform assigned default must not become user_explicit, got recorded=%v err=%v", recorded, err)
	}
	var pref mpentity.UserDefaultPreference
	db.Where("user_id = ?", "user-1").First(&pref)
	if pref.Origin != mpentity.OriginPlatformDefault {
		t.Errorf("expected the origin to stay platform_default, got %s", pref.Origin)
	}
}

func TestRecordObservedUserChoice_UserSwitchingAwayFromThePlatformDefaultIsRecorded(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	createBareTestTenant(db, observedTenant)
	createTestUserTenantInDB(db, "user-1", observedTenant, "mine@custom@P", "")
	chat := int(rentity.ModelTypeChat)
	if err := guard.RecordPlatformDefault(context.Background(), db, observedTenant, "user-1", chat, "platform@platform@P"); err != nil {
		t.Fatalf("RecordPlatformDefault failed: %v", err)
	}

	recorded, err := guard.RecordObservedUserChoice(context.Background(), db, observedTenant, "user-1", chat, "mine@custom@P", "")
	if err != nil || !recorded {
		t.Fatalf("a real change away from the platform default must be recorded, got recorded=%v err=%v", recorded, err)
	}
	var pref mpentity.UserDefaultPreference
	db.Where("user_id = ?", "user-1").First(&pref)
	if pref.Origin != mpentity.OriginUserExplicit || pref.CurrentModelID != "mine@custom@P" {
		t.Errorf("expected user_explicit with the user's model, got %+v", pref)
	}
}
