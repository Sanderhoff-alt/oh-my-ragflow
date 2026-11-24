// Package publisher rolls platform managed models out to every tenant: it records the model in the
// catalog, tracks the rollout per tenant, and converges tenants through a retrying worker.
package publisher

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const itemInsertBatchSize = 500

var (
	// ErrInvalidModel reports a rejected model name or type.
	ErrInvalidModel = errors.New("invalid model")
	// ErrModelNotInGateway reports that the gateway does not route the model yet.
	ErrModelNotInGateway = errors.New("model is not routable through the gateway")
	// ErrGatewayUnavailable reports that the gateway route check could not be performed.
	ErrGatewayUnavailable = errors.New("gateway unavailable")
	// ErrModelConflict reports a catalog entry that cannot be published again as requested.
	ErrModelConflict = errors.New("model conflicts with the catalog")
	// ErrTaskNotFound reports an unknown publish task.
	ErrTaskNotFound = errors.New("publish task not found")

	modelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
)

// ModelLister lists the models the gateway currently routes.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// Request describes a model to publish. ModelType uses the ragflow ModelType bit flags.
type Request struct {
	ModelName string `json:"model_name"`
	ModelType int    `json:"model_type"`
}

// Result is returned once the publish task has been recorded.
type Result struct {
	TaskID string `json:"task_id"`
	Total  int    `json:"total"`
}

// Publisher records models in the catalog and creates the per-tenant rollout items.
type Publisher struct {
	lister ModelLister
}

// New creates a Publisher that verifies routes through lister.
func New(lister ModelLister) *Publisher {
	return &Publisher{lister: lister}
}

func newID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

func isPublishableType(modelType int) bool {
	switch modelType {
	case int(rentity.ModelTypeChat), int(rentity.ModelTypeEmbedding), int(rentity.ModelTypeRerank):
		return true
	}
	return false
}

// Publish validates the request, checks the gateway route, and in one transaction upserts the catalog
// row, creates the task, and creates an item for every Ready tenant that does not have the model.
// Publishing the same model again is idempotent: it only creates items for tenants still missing it.
func (p *Publisher) Publish(ctx context.Context, db *gorm.DB, req Request) (*Result, error) {
	req.ModelName = strings.TrimSpace(req.ModelName)
	if !modelNamePattern.MatchString(req.ModelName) {
		return nil, fmt.Errorf("%w: model_name must match %s", ErrInvalidModel, modelNamePattern)
	}
	if !isPublishableType(req.ModelType) {
		return nil, fmt.Errorf("%w: model_type must be chat (%d), embedding (%d) or rerank (%d)", ErrInvalidModel,
			int(rentity.ModelTypeChat), int(rentity.ModelTypeEmbedding), int(rentity.ModelTypeRerank))
	}

	names, err := p.lister.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGatewayUnavailable, err)
	}
	routed := false
	for _, n := range names {
		if n == req.ModelName {
			routed = true
			break
		}
	}
	if !routed {
		return nil, fmt.Errorf("%w: %q", ErrModelNotInGateway, req.ModelName)
	}

	result := &Result{TaskID: newID()}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureCatalogEntry(tx, req); err != nil {
			return err
		}

		task := entity.ModelPublishTask{ID: result.TaskID, ModelName: req.ModelName, ModelType: req.ModelType}
		if err := tx.Create(&task).Error; err != nil {
			return err
		}

		tenants, err := readyTenantsMissingModel(tx, req.ModelName, req.ModelType, task.ID)
		if err != nil {
			return err
		}
		if err := insertItems(tx, task.ID, tenants); err != nil {
			return err
		}
		result.Total = len(tenants)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ensureCatalogEntry creates the active catalog row, or accepts an existing active one with the same type.
// model_name is unique, so a concurrent publish of the same name loses the insert and re-reads the winner.
func ensureCatalogEntry(tx *gorm.DB, req Request) error {
	row := entity.PlatformManagedModel{
		ID:        newID(),
		ModelName: req.ModelName,
		ModelType: req.ModelType,
		Status:    entity.ManagedModelStatusActive,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}

	var current entity.PlatformManagedModel
	if err := tx.Where("model_name = ?", req.ModelName).First(&current).Error; err != nil {
		return err
	}
	if current.ModelType != req.ModelType {
		return fmt.Errorf("%w: %q is already published with model_type %d", ErrModelConflict, req.ModelName, current.ModelType)
	}
	if current.Status != entity.ManagedModelStatusActive {
		return fmt.Errorf("%w: %q is %s and must be reactivated instead of published", ErrModelConflict, req.ModelName, current.Status)
	}
	return nil
}

// readyTenantsMissingModel returns Ready tenants that have no tenant_model row (in any status, so a
// retired model is never resurrected) for the model on their platform instance and no item in taskID.
func readyTenantsMissingModel(db *gorm.DB, modelName string, modelType int, taskID string) ([]string, error) {
	var tenants []string
	err := db.Raw(`
SELECT r.tenant_id FROM platform_tenant_reconcile r
WHERE r.status = ?
AND NOT EXISTS (
	SELECT 1 FROM tenant_model_provider p
	JOIN tenant_model_instance i ON i.provider_id = p.id AND i.instance_name = ?
	JOIN tenant_model m ON m.instance_id = i.id AND m.model_name = ? AND m.model_type = ?
	WHERE p.tenant_id = r.tenant_id AND p.provider_name = ?
)
AND NOT EXISTS (
	SELECT 1 FROM platform_model_publish_item it WHERE it.task_id = ? AND it.tenant_id = r.tenant_id
)
ORDER BY r.tenant_id`,
		entity.StatusReady, reconciler.PlatformInstanceName, modelName, modelType,
		reconciler.ProviderOpenAIAPICompatible, taskID,
	).Scan(&tenants).Error
	return tenants, err
}

func insertItems(db *gorm.DB, taskID string, tenants []string) error {
	for start := 0; start < len(tenants); start += itemInsertBatchSize {
		end := start + itemInsertBatchSize
		if end > len(tenants) {
			end = len(tenants)
		}
		items := make([]entity.ModelPublishItem, 0, end-start)
		for _, tenantID := range tenants[start:end] {
			items = append(items, entity.ModelPublishItem{
				TaskID:   taskID,
				TenantID: tenantID,
				Status:   entity.PublishItemPending,
				Version:  1,
			})
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&items).Error; err != nil {
			return err
		}
	}
	return nil
}
