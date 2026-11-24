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
	TaskID        string `json:"task_id"`
	Total         int    `json:"total"`
	AffectedUsers int64  `json:"affected_users_count,omitempty"`
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
	return entity.IsManagedModelType(modelType)
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
		return nil, fmt.Errorf("%w: model_type must be one of %s", ErrInvalidModel, entity.ManagedModelTypesDescription())
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

	var result *Result
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, err := ensureCatalogEntry(tx, req)
		if err != nil {
			return err
		}
		result, err = startRollout(tx, model)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ensureCatalogEntry creates the active catalog row, or accepts an existing active one with the same type.
// model_name is unique, so a concurrent publish of the same name loses the insert and re-reads the winner.
func ensureCatalogEntry(tx *gorm.DB, req Request) (*entity.PlatformManagedModel, error) {
	row := entity.PlatformManagedModel{
		ID:        newID(),
		ModelName: req.ModelName,
		ModelType: req.ModelType,
		Status:    entity.ManagedModelStatusActive,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, err
	}

	var current entity.PlatformManagedModel
	if err := tx.Where("model_name = ?", req.ModelName).First(&current).Error; err != nil {
		return nil, err
	}
	if current.ModelType != req.ModelType {
		return nil, fmt.Errorf("%w: %q is already published with model_type %d", ErrModelConflict, req.ModelName, current.ModelType)
	}
	if current.Status != entity.ManagedModelStatusActive {
		return nil, fmt.Errorf("%w: %q is %s and must be reactivated instead of published", ErrModelConflict, req.ModelName, current.Status)
	}
	return &current, nil
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
