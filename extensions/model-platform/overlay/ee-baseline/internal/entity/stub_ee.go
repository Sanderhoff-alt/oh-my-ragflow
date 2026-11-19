//go:build ee

package entity

// Tenant permission bits.
const (
	TenantPermissionManageMembers int32 = 1 << iota
	TenantPermissionManageRoles
	TenantPermissionOwner
)
