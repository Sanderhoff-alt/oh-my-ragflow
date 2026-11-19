//go:build ee

package router

import "github.com/gin-gonic/gin"

// allowAll is the community gate for a feature toggle: every request passes.
func allowAll() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
	}
}

func ToggleChat() gin.HandlerFunc    { return allowAll() }
func ToggleDataset() gin.HandlerFunc { return allowAll() }
func ToggleSearch() gin.HandlerFunc  { return allowAll() }
func ToggleFile() gin.HandlerFunc    { return allowAll() }
func ToggleMemory() gin.HandlerFunc  { return allowAll() }
func ToggleModel() gin.HandlerFunc   { return allowAll() }
