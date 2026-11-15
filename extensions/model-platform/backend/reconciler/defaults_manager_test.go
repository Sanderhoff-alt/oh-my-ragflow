package reconciler

import (
	"context"
	"testing"

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

func TestDefaultsManager_InitializesCleanUserTenant(t *testing.T) {
	db := setupTestDB(t)
	mgr := NewDefaultsManager()
	ctx := context.Background()

	tenantID := "tenant-clean-1"
	userID := "user-clean-1"
	createBareTestTenant(db, tenantID)
	createTestUserTenantInDB(db, userID, tenantID, "", "")

	cfg := DefaultModelConfig{
		ChatModelID:            "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID:      "tm-chat-1",
		EmbeddingModelID:       "text-embedding-3-small@platform@OpenAI-API-Compatible",
		EmbeddingTenantModelID: "tm-embd-1",
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res.ChatUpdated != 1 {
		t.Errorf("expected ChatUpdated 1, got %d", res.ChatUpdated)
	}
	if res.EmbeddingUpdated != 1 {
		t.Errorf("expected EmbeddingUpdated 1, got %d", res.EmbeddingUpdated)
	}

	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != cfg.ChatModelID {
		t.Errorf("expected LLMID %s, got %s", cfg.ChatModelID, ut.LLMID)
	}
	if ut.TenantLLMID == nil || *ut.TenantLLMID != cfg.ChatTenantModelID {
		t.Errorf("expected TenantLLMID %s, got %v", cfg.ChatTenantModelID, ut.TenantLLMID)
	}
	if ut.EmbdID != cfg.EmbeddingModelID {
		t.Errorf("expected EmbdID %s, got %s", cfg.EmbeddingModelID, ut.EmbdID)
	}
	if ut.TenantEmbdID == nil || *ut.TenantEmbdID != cfg.EmbeddingTenantModelID {
		t.Errorf("expected TenantEmbdID %s, got %v", cfg.EmbeddingTenantModelID, ut.TenantEmbdID)
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
		ChatModelID:            "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID:      "tm-chat-1",
		EmbeddingModelID:       "text-embedding-3-small@platform@OpenAI-API-Compatible",
		EmbeddingTenantModelID: "tm-embd-1",
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

	if res2.ChatUpdated != 0 {
		t.Errorf("expected 0 chat updates on second run, got %d", res2.ChatUpdated)
	}
	if res2.EmbeddingUpdated != 0 {
		t.Errorf("expected 0 embedding updates on second run, got %d", res2.EmbeddingUpdated)
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
	createTestUserTenantInDB(db, userID, tenantID, customChat, "") // Custom chat, empty embedding

	cfg := DefaultModelConfig{
		ChatModelID:            "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID:      "tm-chat-1",
		EmbeddingModelID:       "text-embedding-3-small@platform@OpenAI-API-Compatible",
		EmbeddingTenantModelID: "tm-embd-1",
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	// Chat should NOT be updated (user selection preserved)
	if res.ChatUpdated != 0 {
		t.Errorf("expected ChatUpdated 0 (protected), got %d", res.ChatUpdated)
	}
	// Embedding should be updated (was empty)
	if res.EmbeddingUpdated != 1 {
		t.Errorf("expected EmbeddingUpdated 1, got %d", res.EmbeddingUpdated)
	}

	var ut rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", userID, tenantID).First(&ut)
	if ut.LLMID != customChat {
		t.Errorf("custom LLMID was overwritten! expected %s, got %s", customChat, ut.LLMID)
	}
	if ut.EmbdID != cfg.EmbeddingModelID {
		t.Errorf("expected initialized EmbdID %s, got %s", cfg.EmbeddingModelID, ut.EmbdID)
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
		ChatModelID:            "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID:      "tm-chat-1",
		EmbeddingModelID:       "text-embedding-3-small@platform@OpenAI-API-Compatible",
		EmbeddingTenantModelID: "tm-embd-1",
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res.ChatUpdated != 0 || res.EmbeddingUpdated != 0 {
		t.Errorf("both custom models must be preserved! got chat=%d, embd=%d", res.ChatUpdated, res.EmbeddingUpdated)
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
		ChatModelID:       "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID: "tm-chat-1",
	}

	res, err := mgr.InitializeTenantDefaults(ctx, db, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeTenantDefaults failed: %v", err)
	}

	if res.ChatUpdated != 1 {
		t.Errorf("expected 1 update (clean user only), got %d", res.ChatUpdated)
	}

	var utClean, utCustom rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", uClean, tenantID).First(&utClean)
	db.Where("user_id = ? AND tenant_id = ?", uCustom, tenantID).First(&utCustom)

	if utClean.LLMID != cfg.ChatModelID {
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
		ChatModelID:       "gpt-4o-mini@platform@OpenAI-API-Compatible",
		ChatTenantModelID: "tm-chat-1",
	}

	// Target only u1
	res, err := mgr.InitializeUserDefaults(ctx, db, u1, tenantID, cfg)
	if err != nil {
		t.Fatalf("InitializeUserDefaults failed: %v", err)
	}
	if res.ChatUpdated != 1 {
		t.Errorf("expected 1 update for targeted user, got %d", res.ChatUpdated)
	}

	var ut1, ut2 rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", u1, tenantID).First(&ut1)
	db.Where("user_id = ? AND tenant_id = ?", u2, tenantID).First(&ut2)

	if ut1.LLMID != cfg.ChatModelID {
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
	createTestTenantInDB(db, tenantID) // creates tenant and owner user_tenant with empty llm_id

	// Reconcile tenant
	if err := rec.Reconcile(ctx, db, tenantID); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Verify user_tenant has platform default models populated
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
	// Allow only chat model, omit embedding model
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

	chatTenantID := "tm-chat-owner"
	embdTenantID := "tm-embd-owner"

	ownerUT := rentity.UserTenant{
		ID:           "ut-" + ownerID,
		UserID:       ownerID,
		TenantID:     tenantID,
		Role:         "owner",
		LLMID:        "gpt-4o@platform@OpenAI-API-Compatible",
		TenantLLMID:  &chatTenantID,
		EmbdID:       "text-embedding-3-small@platform@OpenAI-API-Compatible",
		TenantEmbdID: &embdTenantID,
		RerankID:     "bge-reranker@platform",
		ASRID:        "whisper@platform",
		Img2TxtID:    "qwen-vl@platform",
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
	if res.ChatUpdated != 1 || res.EmbeddingUpdated != 1 {
		t.Errorf("expected 1 chat and 1 embd update, got chat=%d embd=%d", res.ChatUpdated, res.EmbeddingUpdated)
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
		ID:       "ut-" + ownerID,
		UserID:   ownerID,
		TenantID: tenantID,
		Role:     "owner",
		LLMID:    "chat-owner@platform",
		EmbdID:   "embd-owner@platform",
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
	if res.ChatUpdated != 0 {
		t.Errorf("expected 0 chat updates for custom user, got %d", res.ChatUpdated)
	}
	if res.EmbeddingUpdated != 1 {
		t.Errorf("expected 1 embd update for empty embd, got %d", res.EmbeddingUpdated)
	}

	var updatedMember rentity.UserTenant
	db.Where("user_id = ? AND tenant_id = ?", memberID, tenantID).First(&updatedMember)

	if updatedMember.LLMID != customChat {
		t.Errorf("expected custom choice %s preserved, got %s", customChat, updatedMember.LLMID)
	}
	if updatedMember.EmbdID != ownerUT.EmbdID {
		t.Errorf("expected missing embd inherited %s, got %s", ownerUT.EmbdID, updatedMember.EmbdID)
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
	if res.ChatUpdated != 0 || res.EmbeddingUpdated != 0 {
		t.Errorf("expected 0 updates when owner absent, got chat=%d embd=%d", res.ChatUpdated, res.EmbeddingUpdated)
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
