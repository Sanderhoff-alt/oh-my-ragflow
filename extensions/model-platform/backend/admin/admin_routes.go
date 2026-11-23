package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupModelPlatformAdminMiddleware configures model-platform specific middleware for the admin engine.
func SetupModelPlatformAdminMiddleware(engine *gin.Engine) {
	// Placeholder for admin-specific middleware if needed.
}

// RegisterModelPlatformAdminRoutes mounts model-platform admin management routes under the protected admin group.
func RegisterModelPlatformAdminRoutes(protected *gin.RouterGroup, customDB ...*gorm.DB) {
	if protected == nil {
		return
	}

	modelPlatform := protected.Group("/model-platform")
	{
		modelPlatform.GET("/health", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"status":  "ok",
				"service": "model-platform-admin",
			})
		})

		// Metering and billing management endpoints
		modelPlatform.GET("/usage/daily", GetDailyUsageHandler(customDB...))
		modelPlatform.GET("/usage/summary", GetUsageSummaryHandler(customDB...))
		modelPlatform.GET("/usage/audit", AuditUsageHandler(nil, customDB...))
	}
}
