//go:build ee

package document

import (
	"testing"

	"gorm.io/gorm"
)

// seedDocumentPreviewMemberAccessEE does nothing: the community edition lets
// tenant members read a tenant dataset without a grant row or feature role.
func seedDocumentPreviewMemberAccessEE(_ *testing.T, _ *gorm.DB) {
}
