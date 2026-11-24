package admin

import (
	"net/http"
	"strconv"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	defaultTenantPageSize = 100
	maxTenantPageSize     = 1000
)

// tenantKeyItem is the credential state of one managed tenant. It carries a fingerprint of the key hash,
// never the key.
type tenantKeyItem struct {
	TenantID       string     `json:"tenant_id"`
	Status         string     `json:"status"`
	KeyAlias       string     `json:"key_alias"`
	KeyFingerprint string     `json:"key_fingerprint"`
	KeyRotatedAt   *time.Time `json:"key_rotated_at,omitempty"`
	KeyRotatedBy   string     `json:"key_rotated_by,omitempty"`
	// KeyDirtySince is set while a rotation has not finished revoking the superseded keys.
	KeyDirtySince *time.Time `json:"key_dirty_since,omitempty"`
}

// ListTenantKeysHandler lists the managed tenants with the state of their gateway key, so that operators and
// tooling such as the batch rotation script can see which tenants a rotation would affect.
func ListTenantKeysHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		status := entity.ReconcileStatus(c.DefaultQuery("status", string(entity.StatusReady)))
		after := c.Query("after")
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", strconv.Itoa(defaultTenantPageSize)))
		if pageSize < 1 {
			pageSize = defaultTenantPageSize
		}
		if pageSize > maxTenantPageSize {
			pageSize = maxTenantPageSize
		}

		records, total, err := dao.NewReconcileDAO().ListByStatus(c.Request.Context(), db, status, after, pageSize)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to list tenants"})
			return
		}

		items := make([]tenantKeyItem, 0, len(records))
		for _, rec := range records {
			fingerprint := rec.KeyHash
			if len(fingerprint) > 8 {
				fingerprint = fingerprint[:8]
			}
			items = append(items, tenantKeyItem{
				TenantID:       rec.TenantID,
				Status:         string(rec.Status),
				KeyAlias:       rec.GatewayKeyAlias,
				KeyFingerprint: fingerprint,
				KeyRotatedAt:   rec.KeyRotatedAt,
				KeyRotatedBy:   rec.KeyRotatedBy,
				KeyDirtySince:  rec.KeyDirtySince,
			})
		}
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": gin.H{"total": total, "page_size": pageSize, "items": items}})
	}
}
