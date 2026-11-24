package admin

import (
	"errors"
	"net/http"
	"strconv"

	"ragflow/extensions/model-platform/backend/publisher"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PublishModelHandler onboards a model: it records it in the catalog and starts the rollout to all tenants.
// A nil publisher means the gateway is not configured.
func PublishModelHandler(pub *publisher.Publisher, customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil || pub == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"code": 503, "message": "model publishing is unavailable: gateway or database not configured"})
			return
		}

		var req publisher.Request
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": "invalid request body"})
			return
		}

		res, err := pub.Publish(c.Request.Context(), db, req)
		if err != nil {
			status, message := publishErrorResponse(err)
			c.JSON(status, gin.H{"code": status, "message": message})
			return
		}

		c.JSON(http.StatusAccepted, gin.H{"code": http.StatusAccepted, "data": res})
	}
}

func publishErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, publisher.ErrInvalidModel):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, publisher.ErrModelNotInGateway):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, publisher.ErrModelConflict):
		return http.StatusConflict, err.Error()
	case errors.Is(err, publisher.ErrGatewayUnavailable):
		return http.StatusBadGateway, "gateway unavailable: could not verify the model route"
	default:
		return http.StatusInternalServerError, "failed to publish model"
	}
}

// GetPublishTaskHandler returns the rollout progress of a publish task and a page of failed tenants.
func GetPublishTaskHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
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

		taskID := c.Param("id")
		progress, err := publisher.GetProgress(c.Request.Context(), db, taskID)
		if err != nil {
			respondTaskError(c, err, "failed to query publish progress")
			return
		}
		failed, failedTotal, err := publisher.ListFailedItems(c.Request.Context(), db, taskID, page, pageSize)
		if err != nil {
			respondTaskError(c, err, "failed to query failed tenants")
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"code": http.StatusOK,
			"data": gin.H{
				"progress": progress,
				"failed_items": gin.H{
					"total":     failedTotal,
					"page":      page,
					"page_size": pageSize,
					"items":     failed,
				},
			},
		})
	}
}

// RetryPublishTaskHandler moves the failed tenants of a task back to pending.
func RetryPublishTaskHandler(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db := resolveDB(customDB...)
		if db == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": "database unavailable"})
			return
		}

		reset, err := publisher.RetryFailed(c.Request.Context(), db, c.Param("id"))
		if err != nil {
			respondTaskError(c, err, "failed to retry publish task")
			return
		}

		c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "data": gin.H{"retried": reset}})
	}
}

func respondTaskError(c *gin.Context, err error, fallback string) {
	if errors.Is(err, publisher.ErrTaskNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "message": "publish task not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": fallback})
}
