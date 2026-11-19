//go:build ee

package document

import (
	"context"

	"ragflow/internal/entity"

	"gorm.io/gorm"
)

// grantDocumentOwnerEE grants nothing: the community edition has no document
// permission records.
func grantDocumentOwnerEE(context.Context, *gorm.DB, *entity.Document) error { return nil }
