package admin

import (
	"net/http"
	"strconv"
	"strings"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/metering"
	"ragflow/internal/dao"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func resolveDB(customDB ...*gorm.DB) *gorm.DB {
	if len(customDB) > 0 && customDB[0] != nil {
		return customDB[0]
	}
	return dao.DB
}

// GetDailyUsageHandler retrieves paginated daily model usage records with filtering.
func GetDailyUsageHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		tenantID := c.Query("tenant_id")
		modelName := c.Query("model_name")
		startDate := c.Query("start_date")
		endDate := c.Query("end_date")

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if pageSize < 1 {
			pageSize = 20
		} else if pageSize > 100 {
			pageSize = 100
		}

		query := db.WithContext(c.Request.Context()).Model(&entity.TenantUsageDaily{})
		if tenantID != "" {
			query = query.Where("tenant_id = ?", tenantID)
		}
		if modelName != "" {
			query = query.Where("model_name = ?", modelName)
		}
		if startDate != "" {
			query = query.Where("usage_date >= ?", startDate)
		}
		if endDate != "" {
			query = query.Where("usage_date <= ?", endDate)
		}

		var total int64
		if err := query.Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to count records"})
			return
		}

		offset := (page - 1) * pageSize
		items := make([]entity.TenantUsageDaily, 0)
		err := query.Order("usage_date DESC, tenant_id ASC, model_name ASC").
			Limit(pageSize).
			Offset(offset).
			Find(&items).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "failed to query daily usage"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"code": http.StatusOK,
			"data": gin.H{
				"total":     total,
				"page":      page,
				"page_size": pageSize,
				"items":     items,
			},
		})
	}
}

// GetUsageSummaryHandler calculates high-level consumption metrics for a tenant or platform-wide.
func GetUsageSummaryHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		tenantID := c.Query("tenant_id")
		startDate := c.Query("start_date")
		endDate := c.Query("end_date")

		var summary *metering.UsageSummary
		var err error

		if strings.TrimSpace(tenantID) != "" {
			summary, err = metering.QueryUsageSummary(c.Request.Context(), db, tenantID, startDate, endDate)
		} else {
			summary, err = metering.QueryGlobalUsageSummary(c.Request.Context(), db, startDate, endDate)
		}

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"code": http.StatusOK,
			"data": summary,
		})
	}
}

// AuditUsageHandler conducts a live zero-billing and model isolation audit for a tenant.
// If managedModels is nil or empty, it dynamically resolves the tenant's managed platform models from the database.
func AuditUsageHandler(managedModels []string, customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		tenantID := c.Query("tenant_id")
		if strings.TrimSpace(tenantID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    http.StatusBadRequest,
				"message": "tenant_id parameter is required for audit",
			})
			return
		}

		var report *metering.AuditIsolationReport
		var err error
		if len(managedModels) > 0 {
			report, err = metering.AuditTenantSpendIsolation(c.Request.Context(), db, tenantID, managedModels)
		} else {
			report, err = metering.AuditTenantSpendIsolation(c.Request.Context(), db, tenantID)
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"code": http.StatusOK,
			"data": report,
		})
	}
}
