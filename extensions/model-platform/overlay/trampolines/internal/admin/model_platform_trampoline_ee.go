//go:build ee

package admin

import (
	"github.com/gin-gonic/gin"
	mpAdmin "ragflow/extensions/model-platform/backend/admin"
)

// SetupEERouter forwards enterprise admin middleware configuration to model-platform backend.
func SetupEERouter(engine *gin.Engine) {
	mpAdmin.SetupModelPlatformAdminMiddleware(engine)
}

// RegisterEERouter forwards protected enterprise admin route registration to model-platform backend.
func RegisterEERouter(protected *gin.RouterGroup, r *Router) {
	mpAdmin.RegisterModelPlatformAdminRoutes(protected)
}
