//go:build ee

package router

import (
	"github.com/gin-gonic/gin"
	mpRouter "ragflow/extensions/model-platform/backend/router"
)

// SetupEERouter forwards application router setup (including security guards) to model-platform backend.
func SetupEERouter(engine *gin.Engine) {
	mpRouter.SetupModelPlatformAppMiddleware(engine)
}

// RegisterEENoAuthRouter forwards unauthenticated enterprise routes to model-platform backend.
func RegisterEENoAuthRouter(engine *gin.Engine, noAuth *gin.RouterGroup, r *Router) {
	mpRouter.RegisterModelPlatformNoAuthRoutes(engine, noAuth)
}

// RegisterEEProtectedRouter forwards protected enterprise routes to model-platform backend.
func RegisterEEProtectedRouter(protected *gin.RouterGroup, r *Router) {
	mpRouter.RegisterModelPlatformProtectedRoutes(protected)
}

// RegisterEEAuthRouter forwards authenticated enterprise routes to model-platform backend.
func RegisterEEAuthRouter(auth *gin.RouterGroup, r *Router) {
	mpRouter.RegisterModelPlatformAuthRoutes(auth)
}
