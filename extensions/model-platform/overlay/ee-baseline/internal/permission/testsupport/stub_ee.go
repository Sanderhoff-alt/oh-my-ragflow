//go:build ee

package testsupport

import "gorm.io/gorm"

// PrepareEnterprisePermissionFixture is a no-op: the fixtures used by
// permission-aware tests need no role or tenant-membership rows.
func PrepareEnterprisePermissionFixture(_ *gorm.DB, _ string, _ ...string) error {
	return nil
}
