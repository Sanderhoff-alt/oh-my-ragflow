package admin

import (
	"fmt"
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

func parseModelTypeQuery(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	mt, err := strconv.Atoi(raw)
	if err != nil || !entity.IsManagedModelType(mt) {
		return 0, fmt.Errorf("invalid model_type %q: must be one of %s", raw, entity.ManagedModelTypesDescription())
	}
	return mt, nil
}

// GetDailyUsageHandler retrieves paginated daily model usage records with filtering.
func GetDailyUsageHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		modelType, err := parseModelTypeQuery(c.Query("model_type"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": err.Error()})
			return
		}

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

		filter := metering.UsageFilter{
			TenantID:  strings.TrimSpace(c.Query("tenant_id")),
			ModelName: strings.TrimSpace(c.Query("model_name")),
			ModelType: modelType,
			StartDate: strings.TrimSpace(c.Query("start_date")),
			EndDate:   strings.TrimSpace(c.Query("end_date")),
		}

		items, total, err := metering.QueryDailyUsagePage(c.Request.Context(), db, filter, page, pageSize)
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

		modelType, err := parseModelTypeQuery(c.Query("model_type"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": err.Error()})
			return
		}

		filter := metering.UsageFilter{
			TenantID:  strings.TrimSpace(c.Query("tenant_id")),
			ModelName: strings.TrimSpace(c.Query("model_name")),
			ModelType: modelType,
			StartDate: strings.TrimSpace(c.Query("start_date")),
			EndDate:   strings.TrimSpace(c.Query("end_date")),
		}

		summary, err := metering.QueryUsageSummaryFiltered(c.Request.Context(), db, filter)
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
