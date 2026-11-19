//go:build ee

package permission

import (
	"context"

	"gorm.io/gorm"
)

// newPolicyEE reports that the OSS build has no enterprise policy: the caller
// falls back to communityPolicy.
func newPolicyEE(Source) (policy, bool) { return nil, false }

// tenantRoleMembershipEnabled reports whether tenant roles are stored as
// numeric IDs. The OSS build only stores the built-in roles.
func tenantRoleMembershipEnabled(db *gorm.DB) bool { return false }

// isTenantRoleID reports whether roleValue names an enterprise tenant role.
// The OSS build has none.
func isTenantRoleID(ctx context.Context, db *gorm.DB, tenantID, roleValue string) (bool, error) {
	return false, nil
}
