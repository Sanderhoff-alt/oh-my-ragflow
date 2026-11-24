package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/reconciler"
	rentity "ragflow/internal/entity"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	defaultInterval     = 5 * time.Second
	defaultConcurrency  = 8
	defaultMaxRetries   = 20
	defaultLeaseTimeout = 2 * time.Minute
	dueBatchSize        = 100
	lastErrorMaxLen     = 1000
)

// WorkerOption customizes a Worker.
type WorkerOption func(*Worker)

// WithInterval sets the time between worker ticks.
func WithInterval(d time.Duration) WorkerOption { return func(w *Worker) { w.interval = d } }

// WithConcurrency bounds the number of tenants processed in parallel.
func WithConcurrency(n int) WorkerOption { return func(w *Worker) { w.concurrency = n } }

// WithMaxRetries sets the failures after which an item stays failed until a manual retry.
func WithMaxRetries(n int) WorkerOption { return func(w *Worker) { w.maxRetries = n } }

// WithLeaseTimeout sets how long a running item may stay unchanged before it is recovered.
func WithLeaseTimeout(d time.Duration) WorkerOption { return func(w *Worker) { w.leaseTimeout = d } }

// WithBackoffPolicy sets the retry schedule.
func WithBackoffPolicy(p reconciler.BackoffPolicy) WorkerOption {
	return func(w *Worker) { w.policy = p }
}

// Worker converges tenants toward the catalog: it sweeps for tenants that differ from a model's catalog status
// and applies due items. Several replicas can run it because every item is claimed with an optimistic lock.
type Worker struct {
	dbFn         func() *gorm.DB
	client       reconciler.GatewayKeyClient
	embedding    *reconciler.EmbeddingLifecycleManager
	nonEmbedding *reconciler.NonEmbeddingLifecycleManager
	sticky       *reconciler.StickyGuard
	policy       reconciler.BackoffPolicy
	interval     time.Duration
	concurrency  int
	maxRetries   int
	leaseTimeout time.Duration

	sweepAfter string // tenant the next deprecated-model sweep batch resumes after

	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorker creates a Worker. dbFn is evaluated on every tick so the database may become available late.
func NewWorker(dbFn func() *gorm.DB, client reconciler.GatewayKeyClient, opts ...WorkerOption) *Worker {
	w := &Worker{
		dbFn:         dbFn,
		client:       client,
		embedding:    reconciler.NewEmbeddingLifecycleManager(client),
		nonEmbedding: reconciler.NewNonEmbeddingLifecycleManager(client),
		sticky:       reconciler.NewStickyGuard(),
		policy:       reconciler.DefaultBackoffPolicy,
		interval:     defaultInterval,
		concurrency:  defaultConcurrency,
		maxRetries:   defaultMaxRetries,
		leaseTimeout: defaultLeaseTimeout,
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Start runs the worker loop in the background until ctx is cancelled or Stop is called.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			w.RunOnce(loopCtx)
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop ends the worker loop and waits for the current tick to finish.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		cancel, done := w.cancel, w.done
		w.mu.Unlock()
		if cancel == nil {
			return
		}
		cancel()
		<-done
	})
}

// RunOnce performs one convergence pass: recover stale leases, sweep for missing tenants, apply due items.
func (w *Worker) RunOnce(ctx context.Context) {
	base := w.dbFn()
	if base == nil || ctx.Err() != nil {
		return
	}
	db := base.WithContext(ctx).Session(&gorm.Session{Logger: base.Logger.LogMode(logger.Silent)})

	w.recoverStaleLeases(db)
	w.sweep(ctx, db)

	var due []entity.ModelPublishItem
	if err := db.Where("status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?)", entity.PublishItemPending, time.Now()).
		Order("created_at ASC").Limit(dueBatchSize).Find(&due).Error; err != nil {
		return
	}

	sem := make(chan struct{}, max(w.concurrency, 1))
	var wg sync.WaitGroup
	for _, item := range due {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(item entity.ModelPublishItem) {
			defer wg.Done()
			defer func() { <-sem }()
			w.process(ctx, db, item)
		}(item)
	}
	wg.Wait()
}

// recoverStaleLeases returns items whose worker disappeared to pending.
func (w *Worker) recoverStaleLeases(db *gorm.DB) {
	_ = db.Model(&entity.ModelPublishItem{}).
		Where("status = ? AND updated_at < ?", entity.PublishItemRunning, time.Now().Add(-w.leaseTimeout)).
		Updates(map[string]interface{}{
			"status":     entity.PublishItemPending,
			"version":    gorm.Expr("version + 1"),
			"updated_at": time.Now(),
		}).Error
}

// sweep adds items to the latest task of every catalog model for the tenants that still differ from the
// catalog status: Ready tenants missing an active model, and tenants still holding a retired one. This is
// what covers tenants that became Ready after a request, and anything a failed item left behind.
func (w *Worker) sweep(ctx context.Context, db *gorm.DB) {
	var models []entity.PlatformManagedModel
	if err := db.Find(&models).Error; err != nil {
		return
	}
	for _, m := range models {
		var task entity.ModelPublishTask
		res := db.Where("model_name = ? AND model_type = ?", m.ModelName, m.ModelType).
			Order("created_at DESC").Limit(1).Find(&task)
		if res.Error != nil {
			continue
		}
		if res.RowsAffected == 0 {
			// Catalog rows adopted by the backfill have no task yet. The task is only a progress container.
			task = entity.ModelPublishTask{ID: newID(), ModelName: m.ModelName, ModelType: m.ModelType}
			if err := db.Create(&task).Error; err != nil {
				continue
			}
		}
		tenants, err := tenantsToConverge(db, &m, task.ID)
		if err != nil || len(tenants) == 0 {
			continue
		}
		_ = insertItems(db, task.ID, tenants)
	}
	w.sweepDeprecated(ctx, db)
}

// sweepDeprecated finalizes the deprecated embedding rows of retired models once the tenant's knowledge bases
// that kept them readable are gone.
func (w *Worker) sweepDeprecated(ctx context.Context, db *gorm.DB) {
	candidates, err := w.deprecatedCandidates(db, w.sweepAfter)
	if err != nil {
		return
	}
	if len(candidates) == 0 && w.sweepAfter != "" {
		w.sweepAfter = ""
		if candidates, err = w.deprecatedCandidates(db, ""); err != nil {
			return
		}
	}
	// Tenants that keep reading a deprecated model stay candidates for a long time. Resuming after the last
	// tenant of the previous batch keeps them from occupying every batch and starving the tenants behind them.
	w.sweepAfter = ""
	if len(candidates) == dueBatchSize {
		w.sweepAfter = candidates[len(candidates)-1].TenantID
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		_ = reconciler.WithInstanceRowLock(ctx, db, c.InstanceID, func() error {
			_, sweepErr := w.embedding.SweepTombstoneCandidates(ctx, db, c.TenantID)
			// A partial sweep still tombstoned some models, whose users must not wait for the others.
			return errors.Join(sweepErr, w.replaceTombstonedEmbeddingDefaults(ctx, db, c.TenantID, c.InstanceID))
		})
	}
}

type deprecatedCandidate struct {
	TenantID   string
	InstanceID string
}

// deprecatedCandidates returns up to one batch of tenants after afterTenant that hold a deprecated embedding
// row of a retired catalog model.
func (w *Worker) deprecatedCandidates(db *gorm.DB, afterTenant string) ([]deprecatedCandidate, error) {
	var candidates []deprecatedCandidate
	err := db.Raw(`
SELECT DISTINCT p.tenant_id AS tenant_id, i.id AS instance_id FROM tenant_model_provider p
JOIN tenant_model_instance i ON i.provider_id = p.id AND i.instance_name = ?
JOIN tenant_model m ON m.instance_id = i.id AND m.model_type = ? AND m.status = 'active'
	AND COALESCE(m.extra, '') LIKE '%"deprecated":true%'
JOIN platform_managed_model c ON c.model_name = m.model_name AND c.status = ?
WHERE p.provider_name = ? AND p.tenant_id > ?
ORDER BY p.tenant_id LIMIT ?`,
		reconciler.PlatformInstanceName, int(rentity.ModelTypeEmbedding), entity.ManagedModelStatusRetired,
		reconciler.ProviderOpenAIAPICompatible, afterTenant, dueBatchSize).Scan(&candidates).Error
	return candidates, err
}

// replaceTombstonedEmbeddingDefaults moves the users of the tenant off every retired embedding model that the
// sweep has just tombstoned.
func (w *Worker) replaceTombstonedEmbeddingDefaults(ctx context.Context, db *gorm.DB, tenantID, instanceID string) error {
	var retired []entity.PlatformManagedModel
	if err := db.Where("status = ? AND model_type = ?", entity.ManagedModelStatusRetired, int(rentity.ModelTypeEmbedding)).
		Find(&retired).Error; err != nil {
		return err
	}
	var errs []error
	for i := range retired {
		if err := w.replaceRetiredDefaults(ctx, db, tenantID, instanceID, &retired[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// process claims one item, applies it, and records the outcome.
func (w *Worker) process(ctx context.Context, db *gorm.DB, item entity.ModelPublishItem) {
	claimed := item.Version + 1
	res := db.Model(&entity.ModelPublishItem{}).
		Where("task_id = ? AND tenant_id = ? AND version = ? AND status = ?", item.TaskID, item.TenantID, item.Version, entity.PublishItemPending).
		Updates(map[string]interface{}{
			"status":     entity.PublishItemRunning,
			"version":    claimed,
			"updated_at": time.Now(),
		})
	if res.Error != nil || res.RowsAffected != 1 {
		return
	}

	stats, err := w.apply(ctx, db, item)

	finish := db.Model(&entity.ModelPublishItem{}).
		Where("task_id = ? AND tenant_id = ? AND version = ?", item.TaskID, item.TenantID, claimed)
	if err == nil {
		_ = finish.Updates(map[string]interface{}{
			"status":          entity.PublishItemSucceeded,
			"last_error":      "",
			"next_retry_at":   nil,
			"version":         claimed + 1,
			"upgraded_count":  stats.upgraded,
			"preserved_count": stats.preserved,
			"updated_at":      time.Now(),
		}).Error
		return
	}

	retries := item.RetryCount + 1
	updates := map[string]interface{}{
		"retry_count": retries,
		"last_error":  truncate(err.Error(), lastErrorMaxLen),
		"version":     claimed + 1,
		"updated_at":  time.Now(),
	}
	if retries >= w.maxRetries {
		updates["status"] = entity.PublishItemFailed
		updates["next_retry_at"] = nil
	} else {
		updates["status"] = entity.PublishItemPending
		updates["next_retry_at"] = time.Now().Add(w.policy.CalculateTier2Backoff(retries - 1))
	}
	_ = finish.Updates(updates).Error
}

// applyStats reports the users a default model upgrade moved and the users whose own choice it kept.
type applyStats struct {
	upgraded  int64
	preserved int64
}

// apply converges one tenant toward the catalog status of the task's model. The status is read now, not when
// the task was created, so an item always chases the latest intent. Every step is idempotent, and the gateway
// whitelist is always changed before the tenant_model row, so a crash in between is repaired by the next attempt.
// When the model is the platform default, the tenant's platform_default users are then moved to it.
func (w *Worker) apply(ctx context.Context, db *gorm.DB, item entity.ModelPublishItem) (applyStats, error) {
	var task entity.ModelPublishTask
	if err := db.Where("id = ?", item.TaskID).First(&task).Error; err != nil {
		return applyStats{}, fmt.Errorf("publish task unavailable: %w", err)
	}
	var catalog []entity.PlatformManagedModel
	if err := db.Where("model_name = ?", task.ModelName).Limit(1).Find(&catalog).Error; err != nil {
		return applyStats{}, err
	}
	if len(catalog) == 0 {
		return applyStats{}, fmt.Errorf("model %q is not in the catalog", task.ModelName)
	}
	model := catalog[0]

	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ? AND provider_name = ?", item.TenantID, reconciler.ProviderOpenAIAPICompatible).
		First(&provider).Error; err != nil {
		return applyStats{}, fmt.Errorf("managed provider unavailable: %w", err)
	}
	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, reconciler.PlatformInstanceName).
		First(&instance).Error; err != nil {
		return applyStats{}, fmt.Errorf("managed platform instance unavailable: %w", err)
	}

	var rows []rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instance.ID, model.ModelName, model.ModelType).
		Limit(1).Find(&rows).Error; err != nil {
		return applyStats{}, err
	}

	if model.Status == entity.ManagedModelStatusRetired {
		return applyStats{}, w.retireTenantModel(ctx, db, item.TenantID, &model, &instance, rows)
	}
	if err := w.ensureActive(ctx, db, item.TenantID, &model, &provider, &instance, rows); err != nil {
		return applyStats{}, err
	}
	if !model.IsDefault {
		return applyStats{}, nil
	}
	return w.upgradeDefault(ctx, db, item.TenantID, &model, &instance)
}

// upgradeDefault moves the tenant's platform_default users to the model, keeping the users who chose a model
// themselves. It only runs for a model that is healthy in the tenant, and is safe to repeat.
func (w *Worker) upgradeDefault(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	model *entity.PlatformManagedModel,
	instance *rentity.TenantModelInstance,
) (applyStats, error) {
	var rows []rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instance.ID, model.ModelName, model.ModelType).
		Limit(1).Find(&rows).Error; err != nil {
		return applyStats{}, err
	}
	if len(rows) == 0 || rows[0].Status != "active" {
		return applyStats{}, nil
	}
	if deprecated, tombstoned := reconciler.RetirementFlags(rows[0].Extra); deprecated || tombstoned {
		return applyStats{}, nil
	}

	ref := managedRef(model.ModelName)
	res, err := w.sticky.UpgradeDefaultModel(ctx, db, tenantID, model.ModelType, ref, rows[0].ID)
	if err != nil {
		return applyStats{}, fmt.Errorf("failed to upgrade the default model: %w", err)
	}
	slog.Info("platform default model upgrade", "tenant_id", tenantID, "model_name", model.ModelName,
		"model_type", model.ModelType, "upgraded_count", res.UpgradedCount, "preserved_count", res.PreservedCount)
	return applyStats{upgraded: res.UpgradedCount, preserved: res.PreservedCount}, nil
}

// ensureActive onboards the model into a tenant that lacks it, or reactivates a row retired earlier.
func (w *Worker) ensureActive(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	model *entity.PlatformManagedModel,
	provider *rentity.TenantModelProvider,
	instance *rentity.TenantModelInstance,
	rows []rentity.TenantModel,
) error {
	if len(rows) > 0 {
		deprecated, tombstoned := reconciler.RetirementFlags(rows[0].Extra)
		if !deprecated && !tombstoned {
			return nil
		}
		return reconciler.WithInstanceRowLock(ctx, db, instance.ID, func() error {
			if model.ModelType == int(rentity.ModelTypeEmbedding) {
				return w.embedding.ReactivateEmbeddingModel(ctx, db, tenantID, model.ModelName)
			}
			_, err := w.nonEmbedding.ReactivateModel(ctx, db, tenantID, model.ModelName, model.ModelType)
			return err
		})
	}

	return reconciler.WithInstanceWhitelistLock(ctx, db, instance.ID, func() error {
		var existing []rentity.TenantModel
		if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instance.ID, model.ModelName, model.ModelType).
			Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}

		if err := reconciler.IncludeModelInWhitelist(ctx, db, w.client, instance, model.ModelName, model.ModelType); err != nil {
			return err
		}

		extra, _ := json.Marshal(map[string]interface{}{"is_managed": true})
		return db.Create(&rentity.TenantModel{
			ID:         newID(),
			ModelName:  model.ModelName,
			ProviderID: provider.ID,
			InstanceID: instance.ID,
			ModelType:  model.ModelType,
			Status:     "active",
			Extra:      string(extra),
		}).Error
	})
}

// retireTenantModel retires the model in one tenant, then moves the users who still default to it once the
// tenant has lost it for good.
func (w *Worker) retireTenantModel(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	model *entity.PlatformManagedModel,
	instance *rentity.TenantModelInstance,
	rows []rentity.TenantModel,
) error {
	if err := w.ensureRetired(ctx, db, tenantID, model, instance, rows); err != nil {
		return err
	}
	return w.replaceRetiredDefaults(ctx, db, tenantID, instance.ID, model)
}

// replaceRetiredDefaults keeps user defaults from pointing at a model the tenant can no longer use. It only acts
// on a tombstoned row: a deprecated embedding model stays readable, so users who default to it keep it. Users
// move to the platform default of the type when the tenant has it, and the default is cleared otherwise.
func (w *Worker) replaceRetiredDefaults(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	instanceID string,
	model *entity.PlatformManagedModel,
) error {
	var rows []rentity.TenantModel
	if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instanceID, model.ModelName, model.ModelType).
		Limit(1).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if _, tombstoned := reconciler.RetirementFlags(rows[0].Extra); !tombstoned {
		return nil
	}

	newRef, newTenantModelID := "", ""
	var defaults []entity.PlatformManagedModel
	if err := db.Where("model_type = ? AND is_default = ? AND status = ?", model.ModelType, true, entity.ManagedModelStatusActive).
		Limit(1).Find(&defaults).Error; err != nil {
		return err
	}
	if len(defaults) > 0 {
		var healthy []rentity.TenantModel
		if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ? AND status = ?",
			instanceID, defaults[0].ModelName, defaults[0].ModelType, "active").Limit(1).Find(&healthy).Error; err != nil {
			return err
		}
		if len(healthy) > 0 {
			if deprecated, tombstoned := reconciler.RetirementFlags(healthy[0].Extra); !deprecated && !tombstoned {
				newRef = managedRef(defaults[0].ModelName)
				newTenantModelID = healthy[0].ID
			}
		}
	}

	moved, err := w.sticky.ReplaceRetiredDefault(ctx, db, tenantID, model.ModelType,
		managedRef(model.ModelName), rows[0].ID, newRef, newTenantModelID)
	if err != nil {
		return fmt.Errorf("failed to replace the retired default model: %w", err)
	}
	if moved > 0 {
		slog.Info("retired default model replaced", "tenant_id", tenantID, "model_name", model.ModelName,
			"model_type", model.ModelType, "replacement", newRef, "users_count", moved)
	}
	return nil
}

func managedRef(modelName string) string {
	return fmt.Sprintf("%s@%s@%s", modelName, reconciler.PlatformInstanceName, reconciler.ProviderOpenAIAPICompatible)
}

// ensureRetired retires a tenant row that is still fully active. An embedding model becomes deprecated while
// the tenant has knowledge bases bound to it and tombstoned otherwise. Other models are tombstoned directly.
func (w *Worker) ensureRetired(
	ctx context.Context,
	db *gorm.DB,
	tenantID string,
	model *entity.PlatformManagedModel,
	instance *rentity.TenantModelInstance,
	rows []rentity.TenantModel,
) error {
	if len(rows) == 0 || rows[0].Status != "active" {
		return nil
	}
	if deprecated, tombstoned := reconciler.RetirementFlags(rows[0].Extra); deprecated || tombstoned {
		return nil
	}
	return reconciler.WithInstanceRowLock(ctx, db, instance.ID, func() error {
		if model.ModelType == int(rentity.ModelTypeEmbedding) {
			_, err := w.embedding.RetireEmbeddingModel(ctx, db, tenantID, model.ModelName)
			return err
		}
		_, err := w.nonEmbedding.RetireModel(ctx, db, tenantID, model.ModelName, model.ModelType)
		return err
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
