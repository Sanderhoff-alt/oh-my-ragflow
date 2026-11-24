package publisher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/migration"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// fakeGateway implements ListModels and the virtual key calls the worker needs.
type fakeGateway struct {
	mu        sync.Mutex
	routed    []string
	listErr   error
	failKeys  map[string]error
	updates   map[string][]string
	callCount int
}

func newFakeGateway(routed ...string) *fakeGateway {
	return &fakeGateway{routed: routed, failKeys: map[string]error{}, updates: map[string][]string{}}
}

func (g *fakeGateway) ListModels(ctx context.Context) ([]string, error) {
	return g.routed, g.listErr
}

func (g *fakeGateway) UpdateKey(ctx context.Context, req *gateway.UpdateKeyRequest) (*gateway.UpdateKeyResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.callCount++
	if err := g.failKeys[req.Key]; err != nil {
		return nil, err
	}
	g.updates[req.Key] = req.Models
	return &gateway.UpdateKeyResponse{Key: req.Key, Models: req.Models}, nil
}

func (g *fakeGateway) GenerateKey(ctx context.Context, req *gateway.GenerateKeyRequest) (*gateway.GenerateKeyResponse, error) {
	return nil, errors.New("not used")
}

func (g *fakeGateway) DeleteKeyByAlias(ctx context.Context, alias string) error {
	return errors.New("not used")
}

func (g *fakeGateway) setFail(key string, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.failKeys[key] = err
}

func (g *fakeGateway) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.callCount
}

func setupPublishDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:publisher_test_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	if err := migration.MigrateReconcileSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate reconcile schema: %v", err)
	}
	if err := migration.MigratePublishSchema(context.Background(), db); err != nil {
		t.Fatalf("failed to migrate publish schema: %v", err)
	}
	if err := db.AutoMigrate(&rentity.TenantModelProvider{}, &rentity.TenantModelInstance{}, &rentity.TenantModel{}); err != nil {
		t.Fatalf("failed to migrate tenant model tables: %v", err)
	}
	return db
}

// seedTenant creates a tenant with a platform instance holding gpt-4o-mini. The virtual key is "sk-<tenant>".
func seedTenant(t *testing.T, db *gorm.DB, tenantID string, status entity.ReconcileStatus) {
	t.Helper()
	rec := entity.TenantReconcileRecord{TenantID: tenantID, Status: status, MaxRetries: 50, Version: 1}
	provider := rentity.TenantModelProvider{ID: "prov-" + tenantID, TenantID: tenantID, ProviderName: reconciler.ProviderOpenAIAPICompatible}
	instance := rentity.TenantModelInstance{ID: "inst-" + tenantID, InstanceName: reconciler.PlatformInstanceName, ProviderID: provider.ID, APIKey: "sk-" + tenantID, Status: "active"}
	base := rentity.TenantModel{ID: "tm-" + tenantID, ModelName: "gpt-4o-mini", ProviderID: provider.ID, InstanceID: instance.ID, ModelType: int(rentity.ModelTypeChat), Status: "active"}
	for _, row := range []interface{}{&rec, &provider, &instance, &base} {
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("failed to seed tenant %s: %v", tenantID, err)
		}
	}
}

func hasModel(t *testing.T, db *gorm.DB, tenantID, modelName string) (rentity.TenantModel, bool) {
	t.Helper()
	var rows []rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ?", "inst-"+tenantID, modelName).Find(&rows).Error; err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if len(rows) == 0 {
		return rentity.TenantModel{}, false
	}
	return rows[0], true
}

var deepseek = Request{ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat)}

func TestPublish_ValidatesRequest(t *testing.T) {
	db := setupPublishDB(t)
	pub := New(newFakeGateway("deepseek-v3"))

	for _, req := range []Request{
		{ModelName: "", ModelType: int(rentity.ModelTypeChat)},
		{ModelName: "bad name!", ModelType: int(rentity.ModelTypeChat)},
		{ModelName: "deepseek-v3", ModelType: 0},
		{ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeImage2Text)},
	} {
		if _, err := pub.Publish(context.Background(), db, req); !errors.Is(err, ErrInvalidModel) {
			t.Errorf("expected ErrInvalidModel for %+v, got %v", req, err)
		}
	}
}

func TestPublish_RequiresGatewayRoute(t *testing.T) {
	db := setupPublishDB(t)

	if _, err := New(newFakeGateway("gpt-4o-mini")).Publish(context.Background(), db, deepseek); !errors.Is(err, ErrModelNotInGateway) {
		t.Errorf("expected ErrModelNotInGateway, got %v", err)
	}

	down := newFakeGateway()
	down.listErr = errors.New("connection refused")
	if _, err := New(down).Publish(context.Background(), db, deepseek); !errors.Is(err, ErrGatewayUnavailable) {
		t.Errorf("expected ErrGatewayUnavailable, got %v", err)
	}

	var count int64
	db.Model(&entity.PlatformManagedModel{}).Count(&count)
	if count != 0 {
		t.Errorf("a rejected publish must not touch the catalog, found %d rows", count)
	}
}

func TestPublish_CreatesItemsOnlyForReadyTenantsMissingModel(t *testing.T) {
	db := setupPublishDB(t)
	seedTenant(t, db, "t-missing", entity.StatusReady)
	seedTenant(t, db, "t-has", entity.StatusReady)
	seedTenant(t, db, "t-degraded", entity.StatusDegraded)
	db.Create(&rentity.TenantModel{ID: "tm-has", ModelName: "deepseek-v3", ProviderID: "prov-t-has", InstanceID: "inst-t-has", ModelType: int(rentity.ModelTypeChat), Status: "disabled"})

	res, err := New(newFakeGateway("deepseek-v3")).Publish(context.Background(), db, deepseek)
	if err != nil {
		t.Fatalf("Publish failed: %v", err)
	}
	if res.Total != 1 {
		t.Fatalf("expected 1 item for the only Ready tenant missing the model, got %d", res.Total)
	}

	var items []entity.ModelPublishItem
	db.Where("task_id = ?", res.TaskID).Find(&items)
	if len(items) != 1 || items[0].TenantID != "t-missing" || items[0].Status != entity.PublishItemPending {
		t.Errorf("unexpected items: %+v", items)
	}

	var catalog entity.PlatformManagedModel
	if err := db.Where("model_name = ?", "deepseek-v3").First(&catalog).Error; err != nil || catalog.Status != entity.ManagedModelStatusActive {
		t.Errorf("expected an active catalog row, got %+v (%v)", catalog, err)
	}
}

func TestPublish_ConcurrentPublishesWithDifferentTypesLeaveOneCatalogRow(t *testing.T) {
	db := setupPublishDB(t)
	pub := New(newFakeGateway("deepseek-v3"))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	types := []int{int(rentity.ModelTypeChat), int(rentity.ModelTypeEmbedding)}
	for i := range types {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = pub.Publish(context.Background(), db, Request{ModelName: "deepseek-v3", ModelType: types[i]})
		}(i)
	}
	wg.Wait()

	conflicts := 0
	for _, err := range errs {
		if errors.Is(err, ErrModelConflict) {
			conflicts++
		} else if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if conflicts != 1 {
		t.Errorf("expected exactly one publish to lose with ErrModelConflict, got %d (%v)", conflicts, errs)
	}
	var rows int64
	db.Model(&entity.PlatformManagedModel{}).Where("model_name = ?", "deepseek-v3").Count(&rows)
	if rows != 1 {
		t.Errorf("expected a single catalog row for the name, got %d", rows)
	}
}

func TestPublish_RejectsConflictingCatalogEntries(t *testing.T) {
	db := setupPublishDB(t)
	pub := New(newFakeGateway("deepseek-v3"))
	db.Create(&entity.PlatformManagedModel{ID: "m1", ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeChat), Status: entity.ManagedModelStatusRetired})

	if _, err := pub.Publish(context.Background(), db, deepseek); !errors.Is(err, ErrModelConflict) {
		t.Errorf("expected ErrModelConflict for a disabled model, got %v", err)
	}
	if _, err := pub.Publish(context.Background(), db, Request{ModelName: "deepseek-v3", ModelType: int(rentity.ModelTypeEmbedding)}); !errors.Is(err, ErrModelConflict) {
		t.Errorf("expected ErrModelConflict for a different model type, got %v", err)
	}
}
