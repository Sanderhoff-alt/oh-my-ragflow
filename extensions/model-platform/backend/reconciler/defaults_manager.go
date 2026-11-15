package reconciler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// DefaultModelConfig defines recommended default models to initialize for a tenant or user.
type DefaultModelConfig struct {
	ChatModelID             string
	ChatTenantModelID       string
	EmbeddingModelID        string
	EmbeddingTenantModelID  string
	RerankModelID           string
	RerankTenantModelID     string
	ASRModelID              string
	ASRTenantModelID        string
	Image2TextModelID       string
	Image2TextTenantModelID string
}

// DefaultsInitResult records the number of user_tenant rows updated for each model type.
type DefaultsInitResult struct {
	ChatUpdated       int64
	EmbeddingUpdated  int64
	RerankUpdated     int64
	ASRUpdated        int64
	Image2TextUpdated int64
}

// DefaultsManager handles atomic and safe initialization of tenant default models.
// It uses conditional atomic SQL to guarantee that user-explicit custom selections
// are 100% protected against accidental overwrites by background reconcilers.
type DefaultsManager struct{}

// NewDefaultsManager creates a new DefaultsManager.
func NewDefaultsManager() *DefaultsManager {
	return &DefaultsManager{}
}

// InitializeTenantDefaults conditionally initializes default models for all users in a tenant.
// It executes atomic UPDATE statements checking for empty or NULL columns,
// ensuring zero rows are touched if the user already configured a model.
func (m *DefaultsManager) InitializeTenantDefaults(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	cfg DefaultModelConfig,
) (*DefaultsInitResult, error) {
	return m.InitializeUserDefaults(ctx, db, "", tenantID, cfg)
}

// InitializeMemberFromOwner conditionally initializes a joining member's empty model defaults
// by inheriting from the tenant owner's configuration. Existing user choices are preserved.
func (m *DefaultsManager) InitializeMemberFromOwner(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
) (*DefaultsInitResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if userID == "" {
		return nil, errors.New("defaults manager requires a non-empty userID")
	}

	var owner rentity.UserTenant
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND role = ?", tenantID, "owner").
		First(&owner).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &DefaultsInitResult{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to lookup tenant owner for tenant %s: %w", tenantID, err)
	}

	cfg := DefaultModelConfig{
		ChatModelID:       owner.LLMID,
		EmbeddingModelID:  owner.EmbdID,
		RerankModelID:     owner.RerankID,
		ASRModelID:        owner.ASRID,
		Image2TextModelID: owner.Img2TxtID,
	}
	if owner.TenantLLMID != nil {
		cfg.ChatTenantModelID = *owner.TenantLLMID
	}
	if owner.TenantEmbdID != nil {
		cfg.EmbeddingTenantModelID = *owner.TenantEmbdID
	}
	if owner.TenantRerankID != nil {
		cfg.RerankTenantModelID = *owner.TenantRerankID
	}
	if owner.TenantASRID != nil {
		cfg.ASRTenantModelID = *owner.TenantASRID
	}
	if owner.TenantImg2TxtID != nil {
		cfg.Image2TextTenantModelID = *owner.TenantImg2TxtID
	}

	return m.InitializeUserDefaults(ctx, db, userID, tenantID, cfg)
}

// InitializeUserDefaults conditionally initializes default models for a specific user (or all users if userID is empty)
// in a tenant. It protects existing user choices with atomic conditional SQL.
func (m *DefaultsManager) InitializeUserDefaults(
	ctx context.Context,
	db *gorm.DB,
	userID string,
	tenantID string,
	cfg DefaultModelConfig,
) (*DefaultsInitResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	result := &DefaultsInitResult{}

	now := time.Now().Local()
	nowMilli := now.UnixMilli()
	nowDate := now.Truncate(time.Second)

	// Helper to apply user filter if specified
	applyUserScope := func(tx *gorm.DB) *gorm.DB {
		tx = tx.Where("tenant_id = ?", tenantID)
		if userID != "" {
			tx = tx.Where("user_id = ?", userID)
		}
		return tx
	}

	// 1. Conditionally initialize Chat Model (llm_id, tenant_llm_id)
	if cfg.ChatModelID != "" {
		chatUpdates := map[string]interface{}{
			"llm_id":      cfg.ChatModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if cfg.ChatTenantModelID != "" {
			chatUpdates["tenant_llm_id"] = &cfg.ChatTenantModelID
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where("(llm_id IS NULL OR llm_id = '')").
			Updates(chatUpdates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize chat default model: %w", res.Error)
		}
		result.ChatUpdated = res.RowsAffected
	}

	// 2. Conditionally initialize Embedding Model (embd_id, tenant_embd_id)
	if cfg.EmbeddingModelID != "" {
		embdUpdates := map[string]interface{}{
			"embd_id":     cfg.EmbeddingModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if cfg.EmbeddingTenantModelID != "" {
			embdUpdates["tenant_embd_id"] = &cfg.EmbeddingTenantModelID
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where("(embd_id IS NULL OR embd_id = '')").
			Updates(embdUpdates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize embedding default model: %w", res.Error)
		}
		result.EmbeddingUpdated = res.RowsAffected
	}

	// 3. Conditionally initialize Rerank Model (rerank_id, tenant_rerank_id)
	if cfg.RerankModelID != "" {
		rerankUpdates := map[string]interface{}{
			"rerank_id":   cfg.RerankModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if cfg.RerankTenantModelID != "" {
			rerankUpdates["tenant_rerank_id"] = &cfg.RerankTenantModelID
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where("(rerank_id IS NULL OR rerank_id = '')").
			Updates(rerankUpdates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize rerank default model: %w", res.Error)
		}
		result.RerankUpdated = res.RowsAffected
	}

	// 4. Conditionally initialize ASR Model (asr_id, tenant_asr_id)
	if cfg.ASRModelID != "" {
		asrUpdates := map[string]interface{}{
			"asr_id":      cfg.ASRModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if cfg.ASRTenantModelID != "" {
			asrUpdates["tenant_asr_id"] = &cfg.ASRTenantModelID
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where("(asr_id IS NULL OR asr_id = '')").
			Updates(asrUpdates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize asr default model: %w", res.Error)
		}
		result.ASRUpdated = res.RowsAffected
	}

	// 5. Conditionally initialize Image2Text Model (img2txt_id, tenant_img2txt_id)
	if cfg.Image2TextModelID != "" {
		imgUpdates := map[string]interface{}{
			"img2txt_id":  cfg.Image2TextModelID,
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}
		if cfg.Image2TextTenantModelID != "" {
			imgUpdates["tenant_img2txt_id"] = &cfg.Image2TextTenantModelID
		}

		res := applyUserScope(silentDB.Model(&rentity.UserTenant{})).
			Where("(img2txt_id IS NULL OR img2txt_id = '')").
			Updates(imgUpdates)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to atomically initialize image2text default model: %w", res.Error)
		}
		result.Image2TextUpdated = res.RowsAffected
	}

	return result, nil
}
