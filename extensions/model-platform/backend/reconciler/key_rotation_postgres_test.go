package reconciler

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/migration"
	"ragflow/extensions/model-platform/backend/testenv"
	rentity "ragflow/internal/entity"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// postgresTestEnv names a PostgreSQL server for the tests that need real row locks, which SQLite does not
// have. It is not set in CI. Example:
//
//	MODEL_PLATFORM_TEST_POSTGRES="host=127.0.0.1 port=5432 user=postgres password=pw dbname=mp sslmode=disable"
const postgresTestEnv = "MODEL_PLATFORM_TEST_POSTGRES"

// setupPostgresDB opens the server named by postgresTestEnv inside a throwaway schema, so it never touches
// existing tables, and creates the model platform and upstream tenant tables there.
func setupPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(postgresTestEnv)
	if dsn == "" {
		t.Skipf("%s is not set", postgresTestEnv)
	}
	quiet := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	admin, err := gorm.Open(postgres.Open(dsn), quiet)
	if err != nil {
		t.Fatalf("failed to open postgres: %v", err)
	}
	schema := fmt.Sprintf("mp_test_%d", time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	t.Cleanup(func() { admin.Exec("DROP SCHEMA " + schema + " CASCADE") })

	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), quiet)
	if err != nil {
		t.Fatalf("failed to open postgres: %v", err)
	}
	// The upstream entities declare MySQL column types
	db.Exec("CREATE DOMAIN longtext AS text")
	db.Exec("CREATE DOMAIN datetime AS timestamptz")

	ctx := context.Background()
	if err := migration.MigrateReconcileSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}
	if err := migration.MigratePreferenceSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate preference schema: %v", err)
	}
	if err := db.AutoMigrate(&rentity.Tenant{}, &rentity.UserTenant{}, &rentity.TenantModelProvider{},
		&rentity.TenantModelInstance{}, &rentity.TenantModel{}, &rentity.Knowledgebase{}); err != nil {
		t.Fatalf("failed to migrate tenant tables: %v", err)
	}
	if err := migration.MigratePublishSchema(ctx, db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := testenv.SeedPlatformTestModels(db); err != nil {
		t.Fatalf("failed to seed the catalog: %v", err)
	}
	return db
}

// TestKeyRotator_RotatesUnderTheRowLock guards against locking the row a rotation writes: the lifecycle row
// lock is held in its own transaction, so a lock on the instance row would block the rotation's own update
// of api_key until the database gave up, and every rotation would fail.
func TestKeyRotator_RotatesUnderTheRowLock(t *testing.T) {
	db := setupPostgresDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-pg-rotate"
	oldKey := provisionReadyTenant(t, db, m, tenantID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := NewKeyRotator(m, nil).Rotate(ctx, db, tenantID, "admin")
	if err != nil {
		t.Fatalf("rotation failed: %v", err)
	}
	if !res.Converged || instanceKey(t, db, tenantID) == oldKey {
		t.Fatalf("expected a converged rotation to a new key, got %+v", res)
	}
}
