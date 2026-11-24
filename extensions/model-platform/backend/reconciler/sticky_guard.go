package reconciler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// UpgradeResult reports the outcome of a default model upgrade sweep.
type UpgradeResult struct {
	UpgradedCount  int64
	PreservedCount int64
}

// StickyGuard manages user model preferences and enforces sticky protection for
// explicit user selections when the platform upgrades recommended default models.
type StickyGuard struct{}

// NewStickyGuard creates a new StickyGuard.
func NewStickyGuard() *StickyGuard {
	return &StickyGuard{}
}

func generatePrefID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func isSupportedModelType(mt int) bool {
	switch mt {
	case int(rentity.ModelTypeChat),
		int(rentity.ModelTypeEmbedding),
		int(rentity.ModelTypeRerank),
		int(rentity.ModelTypeSpeech2Text),
		int(rentity.ModelTypeImage2Text):
		return true
	default:
		return false
	}
}

// SetUserExplicitPreference records that a user intentionally chose a specific model,
// updates user_tenant, and marks the preference origin as 'user_explicit'.
func (g *StickyGuard) SetUserExplicitPreference(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
	modelType int,
	modelID string,
	tenantModelID string,
) error {
	if db == nil {
		return dao.ErrNilTransaction
	}
	if tenantID == "" {
		return dao.ErrEmptyTenantID
	}
	if userID == "" {
		return errors.New("userID cannot be empty")
	}
	if modelID == "" {
		return errors.New("modelID cannot be empty")
	}
	if !isSupportedModelType(modelType) {
		return fmt.Errorf("unsupported model type: %d", modelType)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	return silentDB.Transaction(func(tx *gorm.DB) error {
		// 1. Verify user is a member of the tenant and lock membership row to serialize against concurrent upgrades
		var member rentity.UserTenant
		memberQuery := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID)
		if tx.Dialector.Name() != "sqlite" {
			memberQuery = memberQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := memberQuery.First(&member).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("user %s is not a member of tenant %s", userID, tenantID)
			}
			return fmt.Errorf("failed to verify user tenant membership: %w", err)
		}

		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)

		// 2. Update user_tenant based on model type (with stale pointer clearing)
		updates := map[string]interface{}{
			"update_time": &nowMilli,
			"update_date": &nowDate,
		}

		var tIDPtr *string
		if tenantModelID != "" {
			tIDPtr = &tenantModelID
		}

		switch modelType {
		case int(rentity.ModelTypeChat):
			updates["llm_id"] = modelID
			updates["tenant_llm_id"] = tIDPtr
		case int(rentity.ModelTypeEmbedding):
			updates["embd_id"] = modelID
			updates["tenant_embd_id"] = tIDPtr
		case int(rentity.ModelTypeRerank):
			updates["rerank_id"] = modelID
			updates["tenant_rerank_id"] = tIDPtr
		case int(rentity.ModelTypeSpeech2Text):
			updates["asr_id"] = modelID
			updates["tenant_asr_id"] = tIDPtr
		case int(rentity.ModelTypeImage2Text):
			updates["img2txt_id"] = modelID
			updates["tenant_img2txt_id"] = tIDPtr
		}

		if err := tx.Model(&rentity.UserTenant{}).
			Where("tenant_id = ? AND user_id = ?", tenantID, userID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update user_tenant: %w", err)
		}

		// 3. Concurrency-safe Upsert platform_user_default_preference with origin = 'user_explicit'
		pref := mpentity.UserDefaultPreference{
			ID:             generatePrefID(),
			TenantID:       tenantID,
			UserID:         userID,
			ModelType:      modelType,
			Origin:         mpentity.OriginUserExplicit,
			CurrentModelID: modelID,
		}
		return tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "model_type"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"origin":           mpentity.OriginUserExplicit,
				"current_model_id": modelID,
			}),
		}).Create(&pref).Error
	})
}

// userTenantSlot returns the model reference and tenant model id a membership currently holds for a model type.
func userTenantSlot(ut *rentity.UserTenant, modelType int) (modelID, tenantModelID string) {
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	switch modelType {
	case int(rentity.ModelTypeChat):
		return ut.LLMID, deref(ut.TenantLLMID)
	case int(rentity.ModelTypeEmbedding):
		return ut.EmbdID, deref(ut.TenantEmbdID)
	case int(rentity.ModelTypeRerank):
		return ut.RerankID, deref(ut.TenantRerankID)
	case int(rentity.ModelTypeSpeech2Text):
		return ut.ASRID, deref(ut.TenantASRID)
	case int(rentity.ModelTypeImage2Text):
		return ut.Img2TxtID, deref(ut.TenantImg2TxtID)
	}
	return "", ""
}

// RecordObservedUserChoice marks a default that a user changed through the upstream API as user_explicit.
// Unlike SetUserExplicitPreference it never writes user_tenant: the value was observed after the upstream
// handler committed it, so only the preference is recorded. Inside one transaction, with the membership row
// locked, it records nothing and returns false when
//   - the stored default is no longer the observed one, because a later write won and must be kept, or
//   - the observed default is the value the platform itself assigned (origin platform_default), because the
//     change then came from a platform upgrade and not from the user.
func (g *StickyGuard) RecordObservedUserChoice(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
	modelType int,
	observedModelID string,
	observedTenantModelID string,
) (bool, error) {
	if db == nil {
		return false, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return false, dao.ErrEmptyTenantID
	}
	if userID == "" || observedModelID == "" {
		return false, errors.New("userID and observedModelID cannot be empty")
	}
	if !isSupportedModelType(modelType) {
		return false, fmt.Errorf("unsupported model type: %d", modelType)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	recorded := false
	err := silentDB.Transaction(func(tx *gorm.DB) error {
		memberQuery := tx.Where("tenant_id = ? AND user_id = ?", tenantID, userID)
		if tx.Dialector.Name() != "sqlite" {
			memberQuery = memberQuery.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var members []rentity.UserTenant
		if err := memberQuery.Limit(1).Find(&members).Error; err != nil {
			return fmt.Errorf("failed to read user tenant membership: %w", err)
		}
		if len(members) == 0 {
			return nil
		}
		if currentID, currentTenantID := userTenantSlot(&members[0], modelType); currentID != observedModelID || currentTenantID != observedTenantModelID {
			return nil
		}

		var prefs []mpentity.UserDefaultPreference
		if err := tx.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, modelType).
			Limit(1).Find(&prefs).Error; err != nil {
			return fmt.Errorf("failed to read user preference: %w", err)
		}
		if len(prefs) > 0 && prefs[0].Origin == mpentity.OriginPlatformDefault && prefs[0].CurrentModelID == observedModelID {
			return nil
		}

		pref := mpentity.UserDefaultPreference{
			ID:             generatePrefID(),
			TenantID:       tenantID,
			UserID:         userID,
			ModelType:      modelType,
			Origin:         mpentity.OriginUserExplicit,
			CurrentModelID: observedModelID,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "model_type"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"origin":           mpentity.OriginUserExplicit,
				"current_model_id": observedModelID,
			}),
		}).Create(&pref).Error; err != nil {
			return err
		}
		recorded = true
		return nil
	})
	return recorded, err
}

// RecordPlatformDefault records a platform-assigned default model in the preference tracking table.
// If the user has already set an explicit choice (origin == 'user_explicit'), this is a NO-OP.
// Uses ON CONFLICT to remain race-free under concurrent execution.
func (g *StickyGuard) RecordPlatformDefault(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
	modelType int,
	modelID string,
) error {
	if db == nil {
		return dao.ErrNilTransaction
	}
	if tenantID == "" {
		return dao.ErrEmptyTenantID
	}
	if userID == "" || modelID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	pref := mpentity.UserDefaultPreference{
		ID:             generatePrefID(),
		TenantID:       tenantID,
		UserID:         userID,
		ModelType:      modelType,
		Origin:         mpentity.OriginPlatformDefault,
		CurrentModelID: modelID,
	}

	return silentDB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "model_type"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"current_model_id": gorm.Expr("CASE WHEN platform_user_default_preference.origin = ? THEN ? ELSE platform_user_default_preference.current_model_id END", mpentity.OriginPlatformDefault, modelID),
		}),
	}).Create(&pref).Error
}

// IsUserExplicit checks whether the user has explicitly configured a custom model for the given type.
func (g *StickyGuard) IsUserExplicit(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	userID string,
	modelType int,
) (bool, error) {
	if db == nil {
		return false, dao.ErrNilTransaction
	}
	if tenantID == "" || userID == "" {
		return false, nil
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})

	var pref mpentity.UserDefaultPreference
	err := silentDB.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, userID, modelType).
		First(&pref).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}

	return pref.Origin == mpentity.OriginUserExplicit, nil
}

// UpgradeDefaultModel rolls out a new platform recommended default model across all users in a tenant.
// Users with origin == 'user_explicit' are strictly preserved (skipped).
// Users with origin == 'platform_default' or no prior record are upgraded to the new default model.
func (g *StickyGuard) UpgradeDefaultModel(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	modelType int,
	newModelID string,
	newTenantModelID string,
) (*UpgradeResult, error) {
	if db == nil {
		return nil, dao.ErrNilTransaction
	}
	if tenantID == "" {
		return nil, dao.ErrEmptyTenantID
	}
	if newModelID == "" {
		return nil, errors.New("newModelID cannot be empty")
	}
	if !isSupportedModelType(modelType) {
		return nil, fmt.Errorf("unsupported model type for upgrade: %d", modelType)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	silentDB := db.WithContext(ctx).Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	result := &UpgradeResult{}

	err := silentDB.Transaction(func(tx *gorm.DB) error {
		// 1. Fetch all user_tenant rows for this tenant
		var memberships []rentity.UserTenant
		if err := tx.Where("tenant_id = ?", tenantID).Find(&memberships).Error; err != nil {
			return fmt.Errorf("failed to query tenant memberships: %w", err)
		}

		now := time.Now().Local()
		nowMilli := now.UnixMilli()
		nowDate := now.Truncate(time.Second)

		var tIDPtr *string
		if newTenantModelID != "" {
			tIDPtr = &newTenantModelID
		}

		for _, m := range memberships {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// 2. Lock membership row before reading preference to serialize against concurrent SetUserExplicitPreference
			var lockedMember rentity.UserTenant
			lockQuery := tx.Where("id = ?", m.ID)
			if tx.Dialector.Name() != "sqlite" {
				lockQuery = lockQuery.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := lockQuery.First(&lockedMember).Error; err != nil {
				return fmt.Errorf("failed to lock membership %s: %w", m.ID, err)
			}

			// 3. Check user preference origin with strict error handling (prevent fail-open)
			var pref mpentity.UserDefaultPreference
			prefQuery := tx.Where("tenant_id = ? AND user_id = ? AND model_type = ?", tenantID, m.UserID, modelType)
			if tx.Dialector.Name() != "sqlite" {
				prefQuery = prefQuery.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			err := prefQuery.First(&pref).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("failed to query user preference for user %s: %w", m.UserID, err)
			}

			if err == nil && pref.Origin == mpentity.OriginUserExplicit {
				// User explicitly selected their model: STICKY PROTECTION ENGAGED!
				result.PreservedCount++
				continue
			}

			// 4. User is on platform_default or has no explicit preference: upgrade model!
			updates := map[string]interface{}{
				"update_time": &nowMilli,
				"update_date": &nowDate,
			}

			switch modelType {
			case int(rentity.ModelTypeChat):
				updates["llm_id"] = newModelID
				updates["tenant_llm_id"] = tIDPtr
			case int(rentity.ModelTypeEmbedding):
				updates["embd_id"] = newModelID
				updates["tenant_embd_id"] = tIDPtr
			case int(rentity.ModelTypeRerank):
				updates["rerank_id"] = newModelID
				updates["tenant_rerank_id"] = tIDPtr
			case int(rentity.ModelTypeSpeech2Text):
				updates["asr_id"] = newModelID
				updates["tenant_asr_id"] = tIDPtr
			case int(rentity.ModelTypeImage2Text):
				updates["img2txt_id"] = newModelID
				updates["tenant_img2txt_id"] = tIDPtr
			}

			if err := tx.Model(&rentity.UserTenant{}).
				Where("id = ?", m.ID).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("failed to upgrade user_tenant %s: %w", m.ID, err)
			}

			// 5. Concurrency-safe upsert of preference tracking record
			newPref := mpentity.UserDefaultPreference{
				ID:             generatePrefID(),
				TenantID:       tenantID,
				UserID:         m.UserID,
				ModelType:      modelType,
				Origin:         mpentity.OriginPlatformDefault,
				CurrentModelID: newModelID,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "tenant_id"}, {Name: "user_id"}, {Name: "model_type"}},
				DoUpdates: clause.Assignments(map[string]interface{}{
					"origin":           gorm.Expr("CASE WHEN platform_user_default_preference.origin = ? THEN ? ELSE platform_user_default_preference.origin END", mpentity.OriginPlatformDefault, mpentity.OriginPlatformDefault),
					"current_model_id": gorm.Expr("CASE WHEN platform_user_default_preference.origin = ? THEN ? ELSE platform_user_default_preference.current_model_id END", mpentity.OriginPlatformDefault, newModelID),
				}),
			}).Create(&newPref).Error; err != nil {
				return fmt.Errorf("failed to update preference record: %w", err)
			}

			result.UpgradedCount++
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
