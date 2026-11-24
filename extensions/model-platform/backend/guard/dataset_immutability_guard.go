package guard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ragflow/internal/dao"
	rentity "ragflow/internal/entity"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	// ErrMsgDatasetEmbeddingImmutable is the error message returned when an update attempts
	// to change the embedding model of a non-empty dataset.
	ErrMsgDatasetEmbeddingImmutable = "Cannot modify embedding model of a dataset that already contains parsed documents or chunks. Create a new dataset to use a different embedding model."

	// ErrMsgDatasetEmbeddingDeprecated is the error message returned when a creation or mutation
	// attempts to bind to a retired/deprecated embedding model.
	ErrMsgDatasetEmbeddingDeprecated = "Embedding model %q is retired/deprecated and cannot be used for knowledgebases. Please choose an active model."

	// ErrMsgTenantDefaultEmbeddingDeprecated is the error message returned when dataset creation
	// omits embedding_model but the tenant's default embedding model is retired/deprecated.
	ErrMsgTenantDefaultEmbeddingDeprecated = "Tenant default embedding model %q is retired/deprecated. Please specify an active embedding model or update tenant default setting."
)

// DatasetEmbeddingGuardMiddleware guards both dataset creation (POST) and mutation (PUT/PATCH),
// enforcing that:
// 1. Deprecated/retired embedding models cannot be bound to new datasets (either explicitly or via default fallback).
// 2. Deprecated/retired embedding models cannot be set on existing datasets (even empty ones).
// 3. Existing non-empty datasets (with documents or chunks) cannot modify their embedding model.
// Accepts an optional *gorm.DB parameter for unit testing; falls back to dao.DB at runtime.
func DatasetEmbeddingGuardMiddleware(customDB ...*gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method
		path := c.Request.URL.Path

		isCreate := method == http.MethodPost && isDatasetCreateRoute(path)
		datasetID, isUpdate := parseDatasetUpdateTarget(c, path)

		// Only guard POST create and PUT/PATCH update routes
		if !isCreate && (!isUpdate || (method != http.MethodPut && method != http.MethodPatch)) {
			c.Next()
			return
		}

		// Inspect request body
		if c.Request.Body == nil {
			c.Next()
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.Next()
			return
		}
		// Restore body for downstream handlers
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

		// Resolve database handle
		var db *gorm.DB
		if len(customDB) > 0 && customDB[0] != nil {
			db = customDB[0]
		} else {
			db = dao.DB
		}

		if db == nil {
			c.Next()
			return
		}

		tenantID := strings.TrimSpace(c.GetString("tenant_id"))
		if tenantID == "" {
			tenantID = strings.TrimSpace(c.GetString("user_id"))
		}

		var payload map[string]interface{}
		if len(bodyBytes) > 0 {
			if err := json.Unmarshal(bodyBytes, &payload); err != nil {
				// Malformed JSON is handled by upstream handler
				c.Next()
				return
			}
		}

		requestedModels := extractEmbeddingFields(payload)

		// CASE 1: Dataset Creation (POST /api/v1/datasets)
		if isCreate {
			targetModel := ""
			if len(requestedModels) > 0 {
				targetModel = requestedModels[0]
			} else if tenantID != "" {
				// No embedding model specified in request; resolve tenant default embedding
				targetModel = resolveTenantDefaultEmbedding(c.Request.Context(), db, tenantID, strings.TrimSpace(c.GetString("user_id")))
				if targetModel != "" && isModelDeprecated(c.Request.Context(), db, tenantID, targetModel) {
					c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
						"code":    http.StatusBadRequest,
						"message": fmt.Sprintf(ErrMsgTenantDefaultEmbeddingDeprecated, targetModel),
					})
					return
				}
			}

			if targetModel != "" && isModelDeprecated(c.Request.Context(), db, tenantID, targetModel) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"code":    http.StatusBadRequest,
					"message": fmt.Sprintf(ErrMsgDatasetEmbeddingDeprecated, targetModel),
				})
				return
			}

			c.Next()
			return
		}

		// CASE 2: Dataset Mutation (PUT/PATCH /api/v1/datasets/:dataset_id)
		if isUpdate {
			if len(requestedModels) == 0 {
				// No embedding field attempted to be mutated
				c.Next()
				return
			}

			// Query existing knowledgebase
			var kb rentity.Knowledgebase
			err = db.WithContext(c.Request.Context()).
				Where("id = ?", datasetID).
				First(&kb).Error
			if err != nil {
				// Dataset not found or DB error; let upstream handler return proper error
				c.Next()
				return
			}

			// If tenantID was not extracted from context, use the dataset's tenant_id
			if tenantID == "" {
				tenantID = kb.TenantID
			}

			// Verify whether ALL provided embedding models match the current bindings.
			isIdentical := true
			for _, m := range requestedModels {
				matches := strings.EqualFold(m, kb.EmbdID) ||
					(kb.TenantEmbdID != nil && strings.EqualFold(m, *kb.TenantEmbdID))
				if !matches {
					isIdentical = false
					break
				}
			}

			if isIdentical {
				// Sending back the existing embedding model(s) unchanged is permitted
				c.Next()
				return
			}

			// Target model is different!
			// Check A: Is target model deprecated? (Closes empty KB mutation bypass)
			for _, m := range requestedModels {
				if isModelDeprecated(c.Request.Context(), db, tenantID, m) {
					c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
						"code":    http.StatusBadRequest,
						"message": fmt.Sprintf(ErrMsgDatasetEmbeddingDeprecated, m),
					})
					return
				}
			}

			// Check B: If requested model is different, check whether dataset has existing content
			isNonEmpty := kb.ChunkNum > 0 || kb.DocNum > 0
			if !isNonEmpty {
				// Also double check document table in case doc_num was not yet aggregated
				var docCount int64
				err := db.WithContext(c.Request.Context()).
					Table("document").
					Where("kb_id = ? AND (status IS NULL OR status != '0')", datasetID).
					Count(&docCount).Error
				if err != nil {
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"code":    http.StatusInternalServerError,
						"message": "Failed to verify dataset document status",
					})
					return
				}
				if docCount > 0 {
					isNonEmpty = true
				}
			}

			if isNonEmpty {
				// Block mutation with HTTP 400 Bad Request
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"code":    http.StatusBadRequest,
					"message": ErrMsgDatasetEmbeddingImmutable,
				})
				return
			}

			// Dataset is completely empty and new model is active: changing embedding model is permitted
			c.Next()
		}
	}
}

// DatasetImmutabilityGuardMiddleware is an alias for DatasetEmbeddingGuardMiddleware for backward compatibility.
func DatasetImmutabilityGuardMiddleware(customDB ...*gorm.DB) gin.HandlerFunc {
	return DatasetEmbeddingGuardMiddleware(customDB...)
}

func isDatasetCreateRoute(path string) bool {
	trimmed := strings.Trim(path, "/")
	return trimmed == "api/v1/datasets" || trimmed == "v1/datasets" || trimmed == "datasets"
}

func parseDatasetUpdateTarget(c *gin.Context, path string) (string, bool) {
	trimmed := strings.Trim(path, "/")
	parts := strings.Split(trimmed, "/")

	for i, part := range parts {
		if (part == "datasets" || part == "dataset") && i+1 < len(parts) {
			targetID := parts[i+1]
			// Strict terminal segment check guarantees no sub-resources are matched
			if i+2 == len(parts) && targetID != "" {
				return targetID, true
			}
		}
	}

	return "", false
}

func extractEmbeddingFields(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	candidateKeys := []string{
		"embd_id",
		"embedding_model",
		"tenant_embd_id",
		"embedding_id",
	}

	var models []string
	for _, k := range candidateKeys {
		if val, ok := payload[k]; ok && val != nil {
			if strVal, isStr := val.(string); isStr {
				trimmed := strings.TrimSpace(strVal)
				if trimmed != "" {
					models = append(models, trimmed)
				}
			}
		}
	}

	return models
}

// resolveTenantDefaultEmbedding returns the default embedding model of the requesting user, which is what the
// upstream uses when a dataset is created without one. Without a known user it falls back to the tenant's
// oldest member.
func resolveTenantDefaultEmbedding(ctx context.Context, db *gorm.DB, tenantID, userID string) string {
	if db == nil || tenantID == "" {
		return ""
	}
	query := db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if userID != "" {
		query = query.Where("user_id = ?", userID)
	} else {
		query = query.Order("create_time ASC")
	}
	var members []rentity.UserTenant
	if err := query.Limit(1).Find(&members).Error; err == nil && len(members) > 0 {
		return members[0].EmbdID
	}
	return ""
}

// isModelDeprecated reports whether the referenced model can no longer be bound to a dataset. The reference is
// a tenant_model id, a bare name, or name@instance@provider. A composite reference only matches its own
// instance, and a bare name is retired only when no row with that name is healthy, so a tenant's own model
// that shares the name of a retired managed one is never blocked.
func isModelDeprecated(ctx context.Context, db *gorm.DB, tenantID, modelRef string) bool {
	if db == nil || modelRef == "" {
		return false
	}
	modelRef = strings.TrimSpace(modelRef)
	baseName, instanceName := modelRef, ""
	if parts := strings.Split(modelRef, "@"); len(parts) >= 2 {
		baseName, instanceName = parts[0], parts[1]
	}

	query := db.WithContext(ctx).Table("tenant_model").Select("tenant_model.*")
	if tenantID != "" {
		query = query.Joins("JOIN tenant_model_provider ON tenant_model_provider.id = tenant_model.provider_id").
			Where("tenant_model_provider.tenant_id = ?", tenantID)
	}
	if instanceName != "" {
		query = query.Joins("JOIN tenant_model_instance ON tenant_model_instance.id = tenant_model.instance_id").
			Where("tenant_model_instance.instance_name = ?", instanceName)
	}
	query = query.Where("(tenant_model.id = ? OR tenant_model.model_name = ?)", modelRef, baseName)

	var rows []rentity.TenantModel
	if err := query.Find(&rows).Error; err != nil || len(rows) == 0 {
		return false
	}
	for _, tm := range rows {
		if isModelRowHealthy(tm) {
			return false
		}
	}
	return true
}

// isModelRowHealthy reports whether a tenant_model row is active and carries no retirement flag.
func isModelRowHealthy(tm rentity.TenantModel) bool {
	if tm.Status != "" && !strings.EqualFold(tm.Status, "active") {
		return false
	}
	if strings.TrimSpace(tm.Extra) == "" {
		return true
	}
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(tm.Extra), &extra); err != nil {
		return true
	}
	return !isExtraFlagTrue(extra["deprecated"]) && !isExtraFlagTrue(extra["tombstoned"]) && !isExtraFlagTrue(extra["read_only"])
}

func isExtraFlagTrue(val interface{}) bool {
	if val == nil {
		return false
	}
	if b, ok := val.(bool); ok {
		return b
	}
	if s, ok := val.(string); ok {
		return strings.EqualFold(s, "true") || s == "1"
	}
	if n, ok := val.(float64); ok {
		return n != 0
	}
	return false
}
