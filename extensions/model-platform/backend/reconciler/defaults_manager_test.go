package reconciler

import (
	"context"
	"testing"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
)

func createBareTestTenant(db *gorm.DB, tenantID string) {
	db.Exec("INSERT INTO tenant (id, status, parser_ids) VALUES (?, '1', '1')", tenantID)
}

func createTestUserTenantInDB(db *gorm.DB, userID, tenantID, llmID, embdID string) {
	ut := rentity.UserTenant{
		ID:       "ut-" + userID,
		UserID:   userID,
		TenantID: tenantID,
		Role:     "owner",
		LLMID:    llmID,
		EmbdID:   embdID,
	}
	db.Create(&ut)
}

func strPtr(s string) *string { return &s }

func TestDefaultsManager_InitializesCleanUserTenant(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-clean-1"
	userID := "user-clean-1"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat:      {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
		rentity.ModelTypeEmbedding: {ModelID: "text-embedding-3-small@platform@OpenAI-API-Compatible", TenantModelID: "tm-embd-1"},
		rentity.ModelTypeTTS:       {ModelID: "tts-1@platform@OpenAI-API-Compatible", TenantModelID: "tm-tts-1"},
		rentity.ModelTypeDecision:  {ModelID: "deepseek-v3@platform@OpenAI-API-Compatible", TenantModelID: "tm-dec-1"},
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	for _, mt := range []rentity.ModelType{
		rentity.ModelTypeChat,
		rentity.ModelTypeEmbedding,
		rentity.ModelTypeTTS,
		rentity.ModelTypeDecision,
	} {
		if res[mt] != 1 {
			t.Errorf("expected 1 update for model type %v, got %d", mt, res[mt])
		}
	}

	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != cfg[rentity.ModelTypeChat].ModelID {
		t.Errorf("expected LLMID %s, got %s", cfg[rentity.ModelTypeChat].ModelID, ut.LLMID)
	}
	if ut.TenantLLMID == nil || *ut.TenantLLMID != "tm-chat-1" {
		t.Errorf("expected TenantLLMID tm-chat-1, got %v", ut.TenantLLMID)
	}
	if ut.EmbdID != cfg[rentity.ModelTypeEmbedding].ModelID {
		t.Errorf("expected EmbdID %s, got %s", cfg[rentity.ModelTypeEmbedding].ModelID, ut.EmbdID)
	}
	if ut.TenantEmbdID == nil || *ut.TenantEmbdID != "tm-embd-1" {
		t.Errorf("expected TenantEmbdID tm-embd-1, got %v", ut.TenantEmbdID)
	}
	if ut.TTSID == nil || *ut.TTSID != cfg[rentity.ModelTypeTTS].ModelID {
		t.Errorf("expected TTSID %s, got %v", cfg[rentity.ModelTypeTTS].ModelID, ut.TTSID)
	}
	if ut.TenantTTSID == nil || *ut.TenantTTSID != "tm-tts-1" {
		t.Errorf("expected TenantTTSID tm-tts-1, got %v", ut.TenantTTSID)
	}
	if ut.DecisionID == nil || *ut.DecisionID != cfg[rentity.ModelTypeDecision].ModelID {
		t.Errorf("expected DecisionID %s, got %v", cfg[rentity.ModelTypeDecision].ModelID, ut.DecisionID)
	}
	if ut.TenantDecisionID == nil || *ut.TenantDecisionID != "tm-dec-1" {
		t.Errorf("expected TenantDecisionID tm-dec-1, got %v", ut.TenantDecisionID)
	}

	// Verify platform_default ledger records exist for TTS and Decision
	for _, mt := range []rentity.ModelType{rentity.ModelTypeTTS, rentity.ModelTypeDecision} {
		var pref mpentity.UserDefaultPreference
		if err := db.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, int(mt)).
			First(&pref).Error; err != nil {
			t.Fatalf("expected platform_default ledger for model type %v: %v", mt, err)
		}
		if pref.Origin != mpentity.OriginPlatformDefault || pref.CurrentModelID != cfg[mt].ModelID {
			t.Errorf("unexpected ledger entry for %v: %+v", mt, pref)
		}
	}
}

func TestDefaultsManager_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-idemp-1"
	userID := "user-idemp-1"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat:      {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
		rentity.ModelTypeEmbedding: {ModelID: "text-embedding-3-small@platform@OpenAI-API-Compatible", TenantModelID: "tm-embd-1"},
		rentity.ModelTypeTTS:       {ModelID: "tts-1@platform@OpenAI-API-Compatible", TenantModelID: "tm-tts-1"},
		rentity.ModelTypeDecision:  {ModelID: "deepseek-v3@platform@OpenAI-API-Compatible", TenantModelID: "tm-dec-1"},
	}

	// First run
	_, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	// Second run should update 0 rows
	res2, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("second run failed: %v", err)
	}

	for _, mt := range []rentity.ModelType{
		rentity.ModelTypeChat,
		rentity.ModelTypeEmbedding,
		rentity.ModelTypeTTS,
		rentity.ModelTypeDecision,
	} {
		if res2[mt] != 0 {
			t.Errorf("expected 0 updates on second run for %v, got %d", mt, res2[mt])
		}
	}
}

func TestDefaultsManager_PreservesUserExplicitCustomModel(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-custom-1"
	userID := "user-custom-1"
	createBareTestTenant(db, tenantID)

	customChat := "claude-3-5-sonnet@custom-inst@anthropic"
	customTTS := "elevenlabs-v2@custom@elevenlabs"
	utSeed := rentity.UserTenant{
		ID:       "ut-" + userID,
		UserID:   userID,
		TenantID: tenantID,
		Role:     "owner",
		LLMID:    customChat,
		TTSID:    strPtr(customTTS),
	}
	db.Create(&utSeed)

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat:      {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
		rentity.ModelTypeEmbedding: {ModelID: "text-embedding-3-small@platform@OpenAI-API-Compatible", TenantModelID: "tm-embd-1"},
		rentity.ModelTypeTTS:       {ModelID: "tts-1@platform@OpenAI-API-Compatible", TenantModelID: "tm-tts-1"},
		rentity.ModelTypeDecision:  {ModelID: "deepseek-v3@platform@OpenAI-API-Compatible", TenantModelID: "tm-dec-1"},
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res[rentity.ModelTypeChat] != 0 {
		t.Errorf("expected ChatUpdated 0 (protected), got %d", res[rentity.ModelTypeChat])
	}
	if res[rentity.ModelTypeTTS] != 0 {
		t.Errorf("expected TTSUpdated 0 (protected), got %d", res[rentity.ModelTypeTTS])
	}
	if res[rentity.ModelTypeEmbedding] != 1 {
		t.Errorf("expected EmbeddingUpdated 1, got %d", res[rentity.ModelTypeEmbedding])
	}
	if res[rentity.ModelTypeDecision] != 1 {
		t.Errorf("expected DecisionUpdated 1, got %d", res[rentity.ModelTypeDecision])
	}

	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != customChat {
		t.Errorf("custom LLMID was overwritten! expected %s, got %s", customChat, ut.LLMID)
	}
	if ut.TTSID == nil || *ut.TTSID != customTTS {
		t.Errorf("custom TTSID was overwritten! expected %s, got %v", customTTS, ut.TTSID)
	}
	if ut.EmbdID != cfg[rentity.ModelTypeEmbedding].ModelID {
		t.Errorf("expected initialized EmbdID %s, got %s", cfg[rentity.ModelTypeEmbedding].ModelID, ut.EmbdID)
	}
	if ut.DecisionID == nil || *ut.DecisionID != cfg[rentity.ModelTypeDecision].ModelID {
		t.Errorf("expected initialized DecisionID %s, got %v", cfg[rentity.ModelTypeDecision].ModelID, ut.DecisionID)
	}
}

func TestDefaultsManager_PreservesBothCustomModels(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-both-custom"
	userID := "user-both-custom"
	createBareTestTenant(db, tenantID)

	customChat := "qwen-max@custom@aliyun"
	customEmbd := "bge-large-zh@custom@aliyun"
	createTestUserTenantInDB(db, userID, tenantID, customChat, customEmbd)

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat:      {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
		rentity.ModelTypeEmbedding: {ModelID: "text-embedding-3-small@platform@OpenAI-API-Compatible", TenantModelID: "tm-embd-1"},
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res[rentity.ModelTypeChat] != 0 || res[rentity.ModelTypeEmbedding] != 0 {
		t.Errorf("both custom models must be preserved! got chat=%d, embd=%d", res[rentity.ModelTypeChat], res[rentity.ModelTypeEmbedding])
	}

	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != customChat || ut.EmbdID != customEmbd {
		t.Errorf("models were modified: chat=%s, embd=%s", ut.LLMID, ut.EmbdID)
	}
}

func TestDefaultsManager_MultipleUsersInTenant(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-multi-users"
	uClean := "user-clean"
	uCustom := "user-custom"

	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, uClean, tenantID, "", "")
	createTestUserTenantInDB(db, uCustom, tenantID, "user-chosen-model", "")

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat: {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res[rentity.ModelTypeChat] != 1 {
		t.Errorf("expected 1 update (clean user only), got %d", res[rentity.ModelTypeChat])
	}

	var utClean, utCustom rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", uClean, tenantID).First(&utClean)
	db.Where("user_id = ? AND tenant_id = ?", uCustom, tenantID).First(&utCustom)

	if utClean.LLMID != cfg[rentity.ModelTypeChat].ModelID {
		t.Errorf("clean user should be initialized to default, got %s", utClean.LLMID)
	}
	if utCustom.LLMID != "user-chosen-model" {
		t.Errorf("custom user should NOT be overwritten, got %s", utCustom.LLMID)
	}
}

func TestDefaultsManager_InitializeTargetedUser(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-targeted"
	u1 := "user-targeted-1"
	u2 := "user-targeted-2"

	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, u1, tenantID, "", "")
	createTestUserTenantInDB(db, u2, tenantID, "", "")

	cfg := DefaultModelConfig{
		rentity.ModelTypeChat: {ModelID: "gpt-4o-mini@platform@OpenAI-API-Compatible", TenantModelID: "tm-chat-1"},
	}

	res, err := mgr.InitializeUserDefaults(ctx, db, u1, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeUserDefaults failed: %v", err)
	}
	if res[rentity.ModelTypeChat] != 1 {
		t.Errorf("expected 1 update for targeted user, got %d", res[rentity.ModelTypeChat])
	}

	var ut1, ut2 rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", u1, tenantID).First(&ut1)
	db.Where("user_id = ? AND tenant_id = ?", u2, tenantID).First(&ut2)

	if ut1.LLMID != cfg[rentity.ModelTypeChat].ModelID {
		t.Errorf("targeted user should be initialized, got %s", ut1.LLMID)
	}
	if ut2.LLMID != "" {
		t.Errorf("non-targeted user should remain empty, got %s", ut2.LLMID)
	}
}

func TestTenantReconciler_InitializesUserDefaultsAutomatically(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", nil)

	ctx := context.Background()
	tenantID := "tenant-rec-defaults"
	createTestTenantInDB(db, tenantID)

	if err := rec.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var ut rentity.UserTenant
	err := db.Where("tenant_id = ?", tenantID).First(&ut).Error
	if err != nil {
		t.Fatalf("failed to query user_tenant: %v", err)
	}

	expectedChat := "gpt-4o-mini@platform@OpenAI-API-Compatible"
	expectedEmbd := "text-embedding-3-small@platform@OpenAI-API-Compatible"

	if ut.LLMID != expectedChat {
		t.Errorf("expected LLMID %s, got %s", expectedChat, ut.LLMID)
	}
	if ut.EmbdID != expectedEmbd {
		t.Errorf("expected EmbdID %s, got %s", expectedEmbd, ut.EmbdID)
	}
	if ut.TenantLLMID == nil || *ut.TenantLLMID == "" {
		t.Error("expected non-empty TenantLLMID pointer")
	}
	if ut.TenantEmbdID == nil || *ut.TenantEmbdID == "" {
		t.Error("expected non-empty TenantEmbdID pointer")
	}
}

func TestTenantReconciler_SkipsUnpermittedModelDefaults(t *testing.T) {
	db := setupTestDB(t)
	mockClient := newMockGatewayClient()
	rec := NewTenantReconciler(mockClient, "http://127.0.0.1:4000", []string{DefaultChatModelName})

	ctx := context.Background()
	tenantID := "tenant-chat-only-rec"
	createTestTenantInDB(db, tenantID)

	if err := rec.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var ut rentity.UserTenant
	err := db.Where("tenant_id = ?", tenantID).First(&ut).Error
	if err != nil {
		t.Fatalf("failed to query user_tenant: %v", err)
	}

	expectedChat := "gpt-4o-mini@platform@OpenAI-API-Compatible"
	if ut.LLMID != expectedChat {
		t.Errorf("expected LLMID %s, got %s", expectedChat, ut.LLMID)
	}
	if ut.EmbdID != "" {
		t.Errorf("expected unpermitted EmbdID to remain empty, got %s", ut.EmbdID)
	}
}

func TestDefaultsManager_InitializeMemberFromOwner_Inherits(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-inherit-test"
	ownerID := "owner-user"
	memberID := "member-user"

	createBareTestTenant(db, tenantID)

	ownerUT := rentity.UserTenant{
		ID:               "ut-" + ownerID,
		UserID:           ownerID,
		TenantID:         tenantID,
		Role:             "owner",
		LLMID:            "gpt-4o@platform@OpenAI-API-Compatible",
		TenantLLMID:      strPtr("tm-chat-owner"),
		EmbdID:           "text-embedding-3-small@platform@OpenAI-API-Compatible",
		TenantEmbdID:     strPtr("tm-embd-owner"),
		RerankID:         "bge-reranker@platform",
		ASRID:            "whisper@platform",
		Img2TxtID:        "qwen-vl@platform",
		TTSID:            strPtr("tts-1@platform@OpenAI-API-Compatible"),
		TenantTTSID:      strPtr("tm-tts-owner"),
		DecisionID:       strPtr("deepseek-v3@platform@OpenAI-API-Compatible"),
		TenantDecisionID: strPtr("tm-dec-owner"),
	}
	db.Create(&ownerUT)

	memberUT := rentity.UserTenant{
		ID:       "ut-" + memberID,
		UserID:   memberID,
		TenantID: tenantID,
		Role:     "normal",
	}
	db.Create(&memberUT)

	res, err := mgr.InitializeMemberFromOwner(ctx, db, tenantID, memberID)
	if err != nil {
		t.Fatalf("InitializeMemberFromOwner failed: %v", err)
	}
	for _, mt := range []rentity.ModelType{
		rentity.ModelTypeChat,
		rentity.ModelTypeEmbedding,
		rentity.ModelTypeTTS,
		rentity.ModelTypeDecision,
	} {
		if res[mt] != 1 {
			t.Errorf("expected 1 inherited update for %v, got %d", mt, res[mt])
		}
	}

	var updatedMember rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", memberID, tenantID).First(&updatedMember)

	if updatedMember.LLMID != ownerUT.LLMID {
		t.Errorf("expected LLMID %s, got %s", ownerUT.LLMID, updatedMember.LLMID)
	}
	if updatedMember.EmbdID != ownerUT.EmbdID {
		t.Errorf("expected EmbdID %s, got %s", ownerUT.EmbdID, updatedMember.EmbdID)
	}
	if updatedMember.RerankID != ownerUT.RerankID {
		t.Errorf("expected RerankID %s, got %s", ownerUT.RerankID, updatedMember.RerankID)
	}
	if updatedMember.ASRID != ownerUT.ASRID {
		t.Errorf("expected ASRID %s, got %s", ownerUT.ASRID, updatedMember.ASRID)
	}
	if updatedMember.Img2TxtID != ownerUT.Img2TxtID {
		t.Errorf("expected Img2TxtID %s, got %s", ownerUT.Img2TxtID, updatedMember.Img2TxtID)
	}
	if updatedMember.TTSID == nil || *updatedMember.TTSID != *ownerUT.TTSID {
		t.Errorf("expected TTSID %s, got %v", *ownerUT.TTSID, updatedMember.TTSID)
	}
	if updatedMember.TenantTTSID == nil || *updatedMember.TenantTTSID != *ownerUT.TenantTTSID {
		t.Errorf("expected TenantTTSID %s, got %v", *ownerUT.TenantTTSID, updatedMember.TenantTTSID)
	}
	if updatedMember.DecisionID == nil || *updatedMember.DecisionID != *ownerUT.DecisionID {
		t.Errorf("expected DecisionID %s, got %v", *ownerUT.DecisionID, updatedMember.DecisionID)
	}
	if updatedMember.TenantDecisionID == nil || *updatedMember.TenantDecisionID != *ownerUT.TenantDecisionID {
		t.Errorf("expected TenantDecisionID %s, got %v", *ownerUT.TenantDecisionID, updatedMember.TenantDecisionID)
	}
}

func TestDefaultsManager_InitializeMemberFromOwner_PreservesCustom(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-preserve-test"
	ownerID := "owner-p"
	memberID := "member-p"

	createBareTestTenant(db, tenantID)

	ownerUT := rentity.UserTenant{
		ID:         "ut-" + ownerID,
		UserID:     ownerID,
		TenantID:   tenantID,
		Role:       "owner",
		LLMID:      "chat-owner@platform",
		EmbdID:     "embd-owner@platform",
		DecisionID: strPtr("decision-owner@platform"),
	}
	db.Create(&ownerUT)

	customChat := "claude-3-5-sonnet@anthropic"
	memberUT := rentity.UserTenant{
		ID:       "ut-" + memberID,
		UserID:   memberID,
		TenantID: tenantID,
		Role:     "normal",
		LLMID:    customChat,
	}
	db.Create(&memberUT)

	res, err := mgr.InitializeMemberFromOwner(ctx, db, tenantID, memberID)
	if err != nil {
		t.Fatalf("InitializeMemberFromOwner failed: %v", err)
	}
	if res[rentity.ModelTypeChat] != 0 {
		t.Errorf("expected 0 chat updates for custom user, got %d", res[rentity.ModelTypeChat])
	}
	if res[rentity.ModelTypeEmbedding] != 1 {
		t.Errorf("expected 1 embd update for empty embd, got %d", res[rentity.ModelTypeEmbedding])
	}
	if res[rentity.ModelTypeDecision] != 1 {
		t.Errorf("expected 1 decision update for empty decision, got %d", res[rentity.ModelTypeDecision])
	}

	var updatedMember rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", memberID, tenantID).First(&updatedMember)

	if updatedMember.LLMID != customChat {
		t.Errorf("expected custom choice %s preserved, got %s", customChat, updatedMember.LLMID)
	}
	if updatedMember.EmbdID != ownerUT.EmbdID {
		t.Errorf("expected missing embd inherited %s, got %s", ownerUT.EmbdID, updatedMember.EmbdID)
	}
	if updatedMember.DecisionID == nil || *updatedMember.DecisionID != *ownerUT.DecisionID {
		t.Errorf("expected missing decision inherited %s, got %v", *ownerUT.DecisionID, updatedMember.DecisionID)
	}
}

func TestDefaultsManager_InitializeMemberFromOwner_NoOwner(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	res, err := mgr.InitializeMemberFromOwner(ctx, db, "tenant-no-owner", "member-1")
	if err != nil {
		t.Fatalf("expected no error when owner not found, got %v", err)
	}
	if res[rentity.ModelTypeChat] != 0 || res[rentity.ModelTypeEmbedding] != 0 {
		t.Errorf("expected 0 updates when owner absent, got %+v", res)
	}
}

func TestDefaultsManager_InitializeMemberFromOwner_Validation(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	if _, err := mgr.InitializeMemberFromOwner(ctx, nil, "t1", "u1"); err == nil {
		t.Error("expected error on nil db")
	}
	if _, err := mgr.InitializeMemberFromOwner(ctx, db, "", "u1"); err == nil {
		t.Error("expected error on empty tenantID")
	}
	if _, err := mgr.InitializeMemberFromOwner(ctx, db, "t1", ""); err == nil {
		t.Error("expected error on empty userID")
	}
}
