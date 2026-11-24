package publisher

import (
	"context"
	"encoding/json"
	"fmt"
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

// Worker converges tenants toward the catalog: it sweeps for Ready tenants missing an active model
// and applies due items. Several replicas can run it because every item is claimed with an optimistic lock.
type Worker struct {
	dbFn         func() *gorm.DB
	client       reconciler.GatewayKeyClient
	policy       reconciler.BackoffPolicy
	interval     time.Duration
	concurrency  int
	maxRetries   int
	leaseTimeout time.Duration

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
	w.sweep(db)

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

// sweep adds items to the latest task of every active catalog model for Ready tenants that still lack it.
// This is what covers tenants that became Ready after the publish request.
func (w *Worker) sweep(db *gorm.DB) {
	var models []entity.PlatformManagedModel
	if err := db.Where("status = ?", entity.ManagedModelStatusActive).Find(&models).Error; err != nil {
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
		tenants, err := readyTenantsMissingModel(db, m.ModelName, m.ModelType, task.ID)
		if err != nil || len(tenants) == 0 {
			continue
		}
		_ = insertItems(db, task.ID, tenants)
	}
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

	err := w.apply(ctx, db, item)

	finish := db.Model(&entity.ModelPublishItem{}).
		Where("task_id = ? AND tenant_id = ? AND version = ?", item.TaskID, item.TenantID, claimed)
	if err == nil {
		_ = finish.Updates(map[string]interface{}{
			"status":        entity.PublishItemSucceeded,
			"last_error":    "",
			"next_retry_at": nil,
			"version":       claimed + 1,
			"updated_at":    time.Now(),
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

// apply makes the model available to one tenant: gateway whitelist first, then the tenant_model row.
// It is idempotent, so a crash after the gateway call is repaired by the next attempt.
func (w *Worker) apply(ctx context.Context, db *gorm.DB, item entity.ModelPublishItem) error {
	var task entity.ModelPublishTask
	if err := db.Where("id = ?", item.TaskID).First(&task).Error; err != nil {
		return fmt.Errorf("publish task unavailable: %w", err)
	}

	var provider rentity.TenantModelProvider
	if err := db.Where("tenant_id = ? AND provider_name = ?", item.TenantID, reconciler.ProviderOpenAIAPICompatible).
		First(&provider).Error; err != nil {
		return fmt.Errorf("managed provider unavailable: %w", err)
	}
	var instance rentity.TenantModelInstance
	if err := db.Where("provider_id = ? AND instance_name = ?", provider.ID, reconciler.PlatformInstanceName).
		First(&instance).Error; err != nil {
		return fmt.Errorf("managed platform instance unavailable: %w", err)
	}

	return reconciler.WithInstanceWhitelistLock(ctx, db, instance.ID, func() error {
		var existing []rentity.TenantModel
		if err := db.Where("instance_id = ? AND model_name = ? AND model_type = ?", instance.ID, task.ModelName, task.ModelType).
			Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}

		if err := reconciler.IncludeModelInWhitelist(ctx, db, w.client, &instance, task.ModelName, task.ModelType); err != nil {
			return err
		}

		extra, _ := json.Marshal(map[string]interface{}{"is_managed": true})
		return db.Create(&rentity.TenantModel{
			ID:         newID(),
			ModelName:  task.ModelName,
			ProviderID: provider.ID,
			InstanceID: instance.ID,
			ModelType:  task.ModelType,
			Status:     "active",
			Extra:      string(extra),
		}).Error
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
