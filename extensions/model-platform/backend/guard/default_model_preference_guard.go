package guard

import (
	"context"
	"net/http"
	"strings"

	mpentity "ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	"ragflow/internal/dao"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// DefaultModelPreferenceMiddleware records a user's own default model change as a
// user_explicit preference so StickyGuard.UpgradeDefaultModel preserves it.
//
// The upstream handlers report failures with HTTP 200 and an error code in the body,
// so the middleware compares the user's user_tenant defaults before and after the
// request instead of trusting the status. Only a changed, non-empty default is
// recorded; re-selecting the current model or clearing a default is not.
//
// Recording goes through StickyGuard.RecordObservedUserChoice, which runs under the membership
// lock and skips a default that a later write has replaced, or that the platform itself assigned.
// Accepts an optional *gorm.DB parameter for unit testing; falls back to dao.DB at runtime.
func DefaultModelPreferenceMiddleware(customDB ...*gorm.DB) gin.HandlerFunc {
	sticky := reconciler.NewStickyGuard()
	return func(c *gin.Context) {
		if !isDefaultModelUpdateRoute(c) {
			c.Next()
			return
		}

		db := dao.DB
		if len(customDB) > 0 && customDB[0] != nil {
			db = customDB[0]
		}
		tenantID := c.GetString("tenant_id")
		userID := c.GetString("user_id")
		if db == nil || tenantID == "" || userID == "" {
			c.Next()
			return
		}

		ctx := c.Request.Context()
		before, err := loadDefaultModelSlots(ctx, db, tenantID, userID)
		if err != nil {
			_ = c.Error(err)
			c.Next()
			return
		}

		c.Next()

		after, err := loadDefaultModelSlots(ctx, db, tenantID, userID)
		if err != nil {
			_ = c.Error(err)
			return
		}
		for modelType, slot := range after {
			if slot.ModelID == "" || slot == before[modelType] {
				continue
			}
			if _, err := sticky.RecordObservedUserChoice(ctx, db, tenantID, userID, modelType, slot.ModelID, slot.TenantModelID); err != nil {
				_ = c.Error(err)
			}
		}
	}
}

// isDefaultModelUpdateRoute matches the upstream endpoints that change a user's default models.
func isDefaultModelUpdateRoute(c *gin.Context) bool {
	if c.Request.Method != http.MethodPatch {
		return false
	}
	p := c.FullPath()
	if p == "" {
		p = c.Request.URL.Path
	}
	p = strings.TrimRight(strings.ToLower(p), "/")
	return p == "/api/v1/models" || p == "/api/v1/models/default"
}

// loadDefaultModelSlots returns the user's current default model per managed model type.
func loadDefaultModelSlots(ctx context.Context, db *gorm.DB, tenantID, userID string) (map[int]mpentity.DefaultSlot, error) {
	var rows []rentity.UserTenant
	if err := db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return map[int]mpentity.DefaultSlot{}, nil
	}
	return mpentity.ReadUserTenantSlots(&rows[0]), nil
}
