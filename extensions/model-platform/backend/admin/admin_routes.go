package admin

import (
	"net/http"
	"os"

	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/publisher"
	"ragflow/extensions/model-platform/backend/reconciler"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// SetupModelPlatformAdminMiddleware configures model-platform specific middleware for the admin engine.
func SetupModelPlatformAdminMiddleware(engine *gin.Engine) {
	// Placeholder for admin-specific middleware if needed.
}

// resolveGatewayClient returns gwClient, or a client built from the LITELLM_* environment, or nil when neither is configured.
func resolveGatewayClient(gwClient gateway.Client) gateway.Client {
	if gwClient != nil {
		return gwClient
	}
	baseURL := os.Getenv("LITELLM_BASE_URL")
	if baseURL == "" {
		return nil
	}
	return gateway.NewClient(gateway.GatewayConfig{
		BaseURL:   baseURL,
		MasterKey: os.Getenv("LITELLM_MASTER_KEY"),
	})
}

// AdminHealthHandler returns health status of the model platform admin service and optionally downstream gateway.
func AdminHealthHandler(gwClient gateway.Client) gin.HandlerFunc {
	client := resolveGatewayClient(gwClient)

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

	client := resolveGatewayClient(gwClient)
	var pub *publisher.Publisher
	var rotator *reconciler.KeyRotator
	if client != nil {
		pub = publisher.New(client)
		rotator = reconciler.NewKeyRotator(client, nil)
	}

	modelPlatform := protected.Group("/model-platform")
	{
		modelPlatform.GET("/health", AdminHealthHandler(gwClient))

		// Model onboarding: catalog entry plus rollout to all tenants, with progress and retry
		modelPlatform.GET("/models", ListManagedModelsHandler(customDB...))
		modelPlatform.POST("/models", PublishModelHandler(pub, customDB...))
		modelPlatform.POST("/models/:id/retire", RetireModelHandler(customDB...))
		modelPlatform.POST("/models/:id/reactivate", ReactivateModelHandler(customDB...))
		modelPlatform.PUT("/models/:id/default", SetDefaultModelHandler(customDB...))
		modelPlatform.GET("/models/publish/tasks/:id", GetPublishTaskHandler(customDB...))
		modelPlatform.POST("/models/publish/tasks/:id/retry", RetryPublishTaskHandler(customDB...))

		// Emergency credential rotation of tenants
		modelPlatform.GET("/tenants", ListTenantKeysHandler(customDB...))
		modelPlatform.POST("/tenants/:tenant_id/rotate-key", RotateTenantKeyHandler(rotator, customDB...))

		// Metering and billing management endpoints
		modelPlatform.GET("/usage/daily", GetDailyUsageHandler(customDB...))
		modelPlatform.GET("/usage/summary", GetUsageSummaryHandler(customDB...))
		modelPlatform.GET("/usage/audit", AuditUsageHandler(nil, customDB...))
	}
}
