//go:build ee

package service

import "testing"

func grantServiceTestResource(_ *testing.T, _ string, _ string, _ string, _ string, _ int32) {
}

func revokeServiceTestResource(_ *testing.T, _ string, _ string, _ string, _ string) {}

func permissionChangeLogTestModels() []interface{} { return nil }
