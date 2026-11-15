package router

import (
	"github.com/gin-gonic/gin"
	"ragflow/extensions/model-platform/backend/guard"
)

// SetupModelPlatformAppMiddleware configures application-level middleware if needed.
func SetupModelPlatformAppMiddleware(engine *gin.Engine) {
	// Global engine-level middleware placeholder
}

// RegisterModelPlatformNoAuthRoutes registers unauthenticated enterprise routes if needed.
func RegisterModelPlatformNoAuthRoutes(engine *gin.Engine, noAuth *gin.RouterGroup) {
	// Placeholder for non-authenticated model platform routes
}

// RegisterModelPlatformProtectedRoutes registers protected enterprise routes and guards.
func RegisterModelPlatformProtectedRoutes(protected *gin.RouterGroup) {
	if protected == nil {
		return
	}
	// Inject the managed provider guard to protect platform models against unauthorized deletion or alteration.
	// Attached to the protected router group so c.GetString("tenant_id") is already populated by AuthMiddleware.
	protected.Use(guard.ManagedProviderGuardMiddleware())

	// Inject the dataset immutability guard to protect existing dataset vector spaces against corrupting model mutations.
	protected.Use(guard.DatasetImmutabilityGuardMiddleware())
}

// RegisterModelPlatformAuthRoutes registers authenticated enterprise routes.
func RegisterModelPlatformAuthRoutes(auth *gin.RouterGroup) {
	// Placeholder for authenticated model platform routes
}
