package admin

import (
	"net/http"
	"os"

	"ragflow/extensions/model-platform/backend/gateway"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupModelPlatformAdminMiddleware configures model-platform specific middleware for the admin engine.
func SetupModelPlatformAdminMiddleware(engine *gin.Engine) {
	// Placeholder for admin-specific middleware if needed.
}

// AdminHealthHandler returns health status of the model platform admin service and optionally downstream gateway.
func AdminHealthHandler(gwClient gateway.Client) gin.HandlerFunc {
	client := gwClient
	if client == nil {
		baseURL := os.Getenv("LITELLM_BASE_URL")
		if baseURL != "" {
			client = gateway.NewClient(gateway.GatewayConfig{
				BaseURL:   baseURL,
				MasterKey: os.Getenv("LITELLM_MASTER_KEY"),
			})
		}
	}

	return func(c *gin.Context) {
		status := gin.H{
			"status":  "ok",
			"service": "model-platform-admin",
		}

		if client != nil {
			if err := client.HealthCheck(c.Request.Context()); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"status":  "degraded",
					"service": "model-platform-admin",
					"gateway": "unreachable",
					"error":   err.Error(),
				})
				return
			}
			status["gateway"] = "healthy"
		}

		c.JSON(http.StatusOK, status)
	}
}

// RegisterModelPlatformAdminRoutes mounts model-platform admin management routes under the protected admin group.
func RegisterModelPlatformAdminRoutes(protected *gin.RouterGroup, customDB ...*gorm.DB) {
	RegisterModelPlatformAdminRoutesWithGateway(protected, nil, customDB...)
}

// RegisterModelPlatformAdminRoutesWithGateway mounts admin routes with an explicit gateway client for deep health probing.
func RegisterModelPlatformAdminRoutesWithGateway(protected *gin.RouterGroup, gwClient gateway.Client, customDB ...*gorm.DB) {
	if protected == nil {
		return
	}

	modelPlatform := protected.Group("/model-platform")
	{
		modelPlatform.GET("/health", AdminHealthHandler(gwClient))

		// Metering and billing management endpoints
		modelPlatform.GET("/usage/daily", GetDailyUsageHandler(customDB...))
		modelPlatform.GET("/usage/summary", GetUsageSummaryHandler(customDB...))
		modelPlatform.GET("/usage/audit", AuditUsageHandler(nil, customDB...))
	}
}
