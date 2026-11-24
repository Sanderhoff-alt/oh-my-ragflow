// Package testenv holds fixtures shared by the model-platform unit and e2e tests. It must not import
// the reconciler package, because white-box reconciler tests import it.
package testenv

import (
	"strings"

	"ragflow/extensions/model-platform/backend/entity"
	rentity "ragflow/internal/entity"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// ChatModel is the chat model of the baseline test catalog, the default of its type.
	ChatModel = "gpt-4o-mini"
	// EmbeddingModel is the embedding model of the baseline test catalog, the default of its type.
	EmbeddingModel = "text-embedding-3-small"
)

func newID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// SeedPlatformTestModels inserts the baseline catalog the tests used to get from hardcoded constants:
// an active default chat model and an active default embedding model. Each model also gets a publish
// task, because the worker sweep attaches rollout items to the latest task of a model.
// It is idempotent. The publish schema must already be migrated.
func SeedPlatformTestModels(db *gorm.DB) error {
	baseline := []entity.PlatformManagedModel{
		{ModelName: ChatModel, ModelType: int(rentity.ModelTypeChat)},
		{ModelName: EmbeddingModel, ModelType: int(rentity.ModelTypeEmbedding)},
	}
	for _, m := range baseline {
		var existing int64
		if err := db.Model(&entity.PlatformManagedModel{}).Where("model_name = ?", m.ModelName).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			continue
		}
		m.ID = newID()
		m.IsDefault = true
		m.Status = entity.ManagedModelStatusActive
		if err := db.Create(&m).Error; err != nil {
			return err
		}
		task := entity.ModelPublishTask{ID: newID(), ModelName: m.ModelName, ModelType: m.ModelType}
		if err := db.Create(&task).Error; err != nil {
			return err
		}
	}
	return nil
}
