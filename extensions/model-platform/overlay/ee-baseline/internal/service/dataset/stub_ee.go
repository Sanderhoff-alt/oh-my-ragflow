//go:build ee

package dataset

import "testing"

// datasetTestExtraModels adds no tables beyond the community schema.
func datasetTestExtraModels() []interface{} { return nil }

// datasetTestMembershipRole keeps the caller's membership role: the community
// edition has no tenant role table.
func datasetTestMembershipRole(_ *testing.T, _ string, role string) string { return role }

// grantDatasetTestResource grants nothing: the community edition has no
// per-resource permission table to seed.
func grantDatasetTestResource(_ *testing.T, _ string, _ string, _ string, _ string, _ ...string) {
}

// enableDatasetTestModelAccess grants nothing: the community edition has no
// model group access control.
func enableDatasetTestModelAccess(_ *testing.T, _ string, _ string, _ string) {}
