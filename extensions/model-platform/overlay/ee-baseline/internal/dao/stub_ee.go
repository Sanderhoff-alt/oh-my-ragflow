//go:build ee

package dao

import (
	"context"

	mpdao "ragflow/extensions/model-platform/backend/dao"
	mpentity "ragflow/extensions/model-platform/backend/entity"
	mpmigration "ragflow/extensions/model-platform/backend/migration"
	"ragflow/internal/entity"

	"gorm.io/gorm"
)

// tenantDAOTestExtraModels registers platform_tenant_reconcile in test migrations.
func tenantDAOTestExtraModels() []interface{} {
	return []interface{}{
		&mpentity.TenantReconcileRecord{},
		&mpentity.UserDefaultPreference{},
		&mpentity.GatewaySpendLog{},
		&mpentity.TenantUsageDaily{},
		&mpentity.PlatformManagedModel{},
		&mpentity.ModelPublishTask{},
		&mpentity.ModelPublishItem{},
	}
}

// permissionDataModelsEE returns no additional entities in the community
// edition.
func permissionDataModelsEE() []interface{} { return nil }

// billingDataModelsEE returns no billing tables in the community edition.
func billingDataModelsEE() []interface{} { return nil }

// migrateBillingSchemaEE applies model platform schema migrations.
func migrateBillingSchemaEE(ctx context.Context, db *gorm.DB) error {
	if err := mpmigration.MigrateReconcileSchema(ctx, db); err != nil {
		return err
	}
	if err := mpmigration.MigratePreferenceSchema(ctx, db); err != nil {
		return err
	}
	if err := mpmigration.MigrateSpendLogSchema(ctx, db); err != nil {
		return err
	}
	if err := mpmigration.MigrateUsageSchema(ctx, db); err != nil {
		return err
	}
	return mpmigration.MigratePublishSchema(ctx, db)
}

// migratePermissionDataEE is a no-op in the community edition.
func migratePermissionDataEE(ctx context.Context, db *gorm.DB) error { return nil }

// initializeTenantDefaultRoleEE initializes platform reconcile record inside the transaction.
func initializeTenantDefaultRoleEE(ctx context.Context, db *gorm.DB, tenant *entity.Tenant) error {
	if tenant == nil || tenant.ID == "" {
		return nil
	}
	return mpdao.OnTenantCreateHook(ctx, db, tenant.ID)
}

// ApplyDefaultTenantInviteEnabled is a no-op in the community edition.
func ApplyDefaultTenantInviteEnabled(ctx context.Context, db *gorm.DB, tenant *entity.Tenant) error {
	return nil
}

// TenantModelGroupDAO owns the enterprise model groups. The community edition
// has none.
type TenantModelGroupDAO struct{}

// NewTenantModelGroupDAO creates the model group DAO.
func NewTenantModelGroupDAO() *TenantModelGroupDAO { return &TenantModelGroupDAO{} }

// TenantModelGroupMapDAO owns the model-to-group mappings.
type TenantModelGroupMapDAO struct{}

// NewTenantModelGroupMapDAO creates the model group mapping DAO.
func NewTenantModelGroupMapDAO() *TenantModelGroupMapDAO { return &TenantModelGroupMapDAO{} }

// BillingProductDAO owns the billing product catalogue, which the community
// edition does not have.
type BillingProductDAO struct{}

// NewBillingProductDAO creates the billing product DAO.
func NewBillingProductDAO() *BillingProductDAO { return &BillingProductDAO{} }

// BillingSubscriptionDAO owns tenant subscriptions, which the community
// edition does not have.
type BillingSubscriptionDAO struct{}

// NewBillingSubscriptionDAO creates the billing subscription DAO.
func NewBillingSubscriptionDAO() *BillingSubscriptionDAO { return &BillingSubscriptionDAO{} }
