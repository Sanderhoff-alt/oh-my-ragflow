package reconciler

import (
	"context"
	"fmt"
	"sync"
	"testing"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/migration"
	rentity "ragflow/internal/entity"
)

func TestStickyGuard_MigrationIdempotency(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	if err := migration.MigratePreferenceSchema(ctx, db); err != nil {
		t.Fatalf("first migration failed: %v", err)
	}
	if err := migration.MigratePreferenceSchema(ctx, db); err != nil {
		t.Fatalf("second migration failed (idempotency check): %v", err)
	}
}

func TestStickyGuard_SetUserExplicitPreference(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-pref-1"
	userID := "user-pref-1"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	customChat := "claude-3-5-sonnet@custom@anthropic"
	tenantModelID := "tm-custom-chat"

	err := guard.SetUserExplicitPreference(
		ctx, db, tenantID, userID,
		int(rentity.ModelTypeChat), customChat, tenantModelID,
	)
	if err != nil {
		t.Fatalf("SetUserExplicitPreference failed: %v", err)
	}

	// 1. Verify user_tenant updated
	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != customChat {
		t.Errorf("expected LLMID %s, got %s", customChat, ut.LLMID)
	}
	if ut.TenantLLMID == nil || *ut.TenantLLMID != tenantModelID {
		t.Errorf("expected TenantLLMID %s, got %v", tenantModelID, ut.TenantLLMID)
	}

	// 2. Verify preference record exists with OriginUserExplicit
	var pref mpentity.UserDefaultPreference
	err = db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, int(rentity.ModelTypeChat)).
		First(&pref).Error
	if err != nil {
		t.Fatalf("failed to query preference record: %v", err)
	}
	if pref.Origin != mpentity.OriginUserExplicit {
		t.Errorf("expected origin user_explicit, got %s", pref.Origin)
	}
	if pref.CurrentModelID != customChat {
		t.Errorf("expected current model %s, got %s", customChat, pref.CurrentModelID)
	}

	// 3. Verify IsUserExplicit returns true
	isExplicit, err := guard.IsUserExplicit(ctx, db, tenantID, userID, int(rentity.ModelTypeChat))
	if err != nil || !isExplicit {
		t.Errorf("expected IsUserExplicit=true, got %v (err=%v)", isExplicit, err)
	}
}

func TestStickyGuard_ClearsStalePointerWhenEmpty(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-stale-ptr"
	userID := "user-stale-ptr"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	// 1. Set model with non-empty tenantModelID
	_ = guard.SetUserExplicitPreference(ctx, db, tenantID, userID, int(rentity.ModelTypeChat), "model-1", "tm-1")
	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.TenantLLMID == nil || *ut.TenantLLMID != "tm-1" {
		t.Fatalf("expected TenantLLMID tm-1, got %v", ut.TenantLLMID)
	}

	// 2. Switch model with EMPTY tenantModelID: must clear pointer!
	err := guard.SetUserExplicitPreference(ctx, db, tenantID, userID, int(rentity.ModelTypeChat), "direct-model-2", "")
	if err != nil {
		t.Fatalf("SetUserExplicitPreference failed: %v", err)
	}

	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != "direct-model-2" {
		t.Errorf("expected LLMID direct-model-2, got %s", ut.LLMID)
	}
	if ut.TenantLLMID != nil {
		t.Errorf("stale pointer was NOT cleared! expected nil, got %v", *ut.TenantLLMID)
	}
}

func TestStickyGuard_NonMemberRejection(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-exists"
	createBareTestTenant(db, tenantID)

	// User not in tenant
	err := guard.SetUserExplicitPreference(ctx, db, tenantID, "user-not-in-tenant", int(rentity.ModelTypeChat), "model", "tm")
	if err == nil {
		t.Fatal("expected error when setting preference for non-member user, got nil")
	}
}

func TestStickyGuard_UnsupportedModelType(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-unsupp"
	userID := "user-unsupp"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	err := guard.SetUserExplicitPreference(ctx, db, tenantID, userID, 9999, "m", "tm")
	if err == nil {
		t.Fatal("expected error for unsupported model type, got nil")
	}

	_, err = guard.UpgradeDefaultModel(ctx, db, tenantID, 9999, "m", "tm")
	if err == nil {
		t.Fatal("expected error for unsupported model type in upgrade, got nil")
	}
}

func TestStickyGuard_RecordPlatformDefault_NoOverwriteExplicit(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-pref-2"
	userID := "user-pref-2"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	// Set explicit choice
	customChat := "gpt-4o@custom@openai"
	_ = guard.SetUserExplicitPreference(ctx, db, tenantID, userID, int(rentity.ModelTypeChat), customChat, "tm-custom")

	// Now try to record a platform default for the same model type
	platformChat := "gpt-4o-mini@platform@OpenAI-API-Compatible"
	err := guard.RecordPlatformDefault(ctx, db, tenantID, userID, int(rentity.ModelTypeChat), platformChat)
	if err != nil {
		t.Fatalf("RecordPlatformDefault failed: %v", err)
	}

	// Preference record MUST remain OriginUserExplicit and current model must NOT be overwritten
	var pref mpentity.UserDefaultPreference
	db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, int(rentity.ModelTypeChat)).
		First(&pref)
	if pref.Origin != mpentity.OriginUserExplicit {
		t.Errorf("expected origin to remain user_explicit, got %s", pref.Origin)
	}
	if pref.CurrentModelID != customChat {
		t.Errorf("expected current model to remain %s, got %s", customChat, pref.CurrentModelID)
	}
}

func TestStickyGuard_ConcurrentRecordPlatformDefault(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-concur-pref"
	userID := "user-concur-pref"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	var wg sync.WaitGroup
	errCh := make(chan error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			modelID := fmt.Sprintf("model-v%d@platform", idx)
			if err := guard.RecordPlatformDefault(ctx, db, tenantID, userID, int(rentity.ModelTypeChat), modelID); err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent RecordPlatformDefault failed with conflict: %v", err)
	}

	var count int64
	db.Model(&mpentity.UserDefaultPreference{}).
		Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, int(rentity.ModelTypeChat)).
		Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 preference record created, got %d", count)
	}
}

func TestStickyGuard_UpgradeDefaultModel_ProtectsExplicitSelections(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-upgrade-test"
	createBareTestTenant(db, tenantID)

	// User A: on platform default
	userA := "user-A"
	createTestUserTenantInDB(db, userA, tenantID, "old-default-chat@platform@OpenAI-API-Compatible", "")
	_ = guard.RecordPlatformDefault(ctx, db, tenantID, userA, int(rentity.ModelTypeChat), "old-default-chat@platform@OpenAI-API-Compatible")

	// User B: explicit user choice
	userB := "user-B"
	customModelB := "my-finetuned-llama3@custom@local"
	createTestUserTenantInDB(db, userB, tenantID, customModelB, "")
	_ = guard.SetUserExplicitPreference(ctx, db, tenantID, userB, int(rentity.ModelTypeChat), customModelB, "tm-custom-b")

	// User C: membership without preference record
	userC := "user-C"
	createTestUserTenantInDB(db, userC, tenantID, "old-default-chat@platform@OpenAI-API-Compatible", "")

	// Platform rolls out upgraded default model: new-gpt-5
	newDefaultChat := "new-gpt-5@platform@OpenAI-API-Compatible"
	newTenantModelID := "tm-new-chat"

	res, err := guard.UpgradeDefaultModel(
		ctx, db, tenantID,
		int(rentity.ModelTypeChat), newDefaultChat, newTenantModelID,
	)
	if err != nil {
		t.Fatalf("UpgradeDefaultModel failed: %v", err)
	}

	if res.UpgradedCount != 2 {
		t.Errorf("expected 2 users upgraded (User A and User C), got %d", res.UpgradedCount)
	}
	if res.PreservedCount != 1 {
		t.Errorf("expected 1 user preserved (User B), got %d", res.PreservedCount)
	}

	// Verify User A was upgraded
	var utA rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userA, tenantID).First(&utA)
	if utA.LLMID != newDefaultChat {
		t.Errorf("User A should be upgraded to %s, got %s", newDefaultChat, utA.LLMID)
	}

	// Verify User B was NOT modified (sticky protection)
	var utB rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userB, tenantID).First(&utB)
	if utB.LLMID != customModelB {
		t.Errorf("User B custom model was corrupted! expected %s, got %s", customModelB, utB.LLMID)
	}

	// Verify User C was upgraded
	var utC rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userC, tenantID).First(&utC)
	if utC.LLMID != newDefaultChat {
		t.Errorf("User C should be upgraded to %s, got %s", newDefaultChat, utC.LLMID)
	}
}

func TestStickyGuard_AllFiveModelTypes_ExplicitAndUpgrade(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	guard := NewStickyGuard()
	ctx := context.Background()

	tenantID := "tenant-all-5-types"
	userID := "user-all-5-types"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	testCases := []struct {
		name      string
		modelType int
		explicitM string
		upgradeM  string
	}{
		{"Chat", int(rentity.ModelTypeChat), "my-chat@openai", "plat-chat-v2@plat"},
		{"Embedding", int(rentity.ModelTypeEmbedding), "my-embd@openai", "plat-embd-v2@plat"},
		{"Rerank", int(rentity.ModelTypeRerank), "my-rerank@cohere", "plat-rerank-v2@plat"},
		{"ASR", int(rentity.ModelTypeSpeech2Text), "my-asr@whisper", "plat-asr-v2@plat"},
		{"Image2Text", int(rentity.ModelTypeImage2Text), "my-vision@claude", "plat-vision-v2@plat"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set explicit
			err := guard.SetUserExplicitPreference(ctx, db, tenantID, userID, tc.modelType, tc.explicitM, "tm-"+tc.name)
			if err != nil {
				t.Fatalf("SetUserExplicitPreference for %s failed: %v", tc.name, err)
			}

			// Attempt upgrade
			res, err := guard.UpgradeDefaultModel(ctx, db, tenantID, tc.modelType, tc.upgradeM, "tm-new")
			if err != nil {
				t.Fatalf("UpgradeDefaultModel for %s failed: %v", tc.name, err)
			}

			if res.PreservedCount != 1 || res.UpgradedCount != 0 {
				t.Errorf("%s explicit model was not preserved! got preserved=%d, upgraded=%d", tc.name, res.PreservedCount, res.UpgradedCount)
			}
		})
	}
}

func TestDefaultsManager_ProvenanceTrackingForAllFiveTypes(t *testing.T) {
	db := setupTestDB(t)
	_ = migration.MigratePreferenceSchema(context.Background(), db)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-defaults-prov"
	userID := "user-defaults-prov"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	cfg := DefaultModelConfig{
		ChatModelID:       "chat@plat",
		EmbeddingModelID:  "embd@plat",
		RerankModelID:     "rerank@plat",
		ASRModelID:        "asr@plat",
		Image2TextModelID: "vision@plat",
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res.ChatUpdated != 1 || res.EmbeddingUpdated != 1 || res.RerankUpdated != 1 || res.ASRUpdated != 1 || res.Image2TextUpdated != 1 {
		t.Fatalf("expected all 5 models updated, got %+v", res)
	}

	// Verify all 5 provenance records exist in platform_user_default_preference with OriginPlatformDefault
	types := []int{
		int(rentity.ModelTypeChat),
		int(rentity.ModelTypeEmbedding),
		int(rentity.ModelTypeRerank),
		int(rentity.ModelTypeSpeech2Text),
		int(rentity.ModelTypeImage2Text),
	}

	for _, mt := range types {
		var pref mpentity.UserDefaultPreference
		err := db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, mt).First(&pref).Error
		if err != nil {
			t.Errorf("missing preference record for model type %d: %v", mt, err)
		}
		if pref.Origin != mpentity.OriginPlatformDefault {
			t.Errorf("expected OriginPlatformDefault for type %d, got %s", mt, pref.Origin)
		}
	}
}
