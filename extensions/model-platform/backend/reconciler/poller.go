package reconciler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/entity"
	"ragflow/extensions/model-platform/backend/event"

	"gorm.io/gorm"
)

// ControllerOption configures the SelfHealingController.
type ControllerOption func(*SelfHealingController)

// WithBackoffPolicy sets a custom BackoffPolicy used when scheduling deferred
// retries in the database.
func WithBackoffPolicy(policy BackoffPolicy) ControllerOption {
	return func(c *SelfHealingController) {
		c.policy = policy
	}
}

// WithPollerInterval sets the Tier-2 level-triggered scanning interval.
func WithPollerInterval(interval time.Duration) ControllerOption {
	return func(c *SelfHealingController) {
		if interval > 0 {
			c.pollerInterval = interval
		}
	}
}

// WithMaxRetries sets the maximum total retries before marking a tenant failed.
func WithMaxRetries(maxRetries int) ControllerOption {
	return func(c *SelfHealingController) {
		if maxRetries > 0 {
			c.maxTotalRetries = maxRetries
		}
	}
}

// WithDisablePoller runs only the Tier-1 TenantCreated event listener without
// launching the Tier-2 periodic polling goroutine. This is used by the Admin
// server process so admin-created tenants get immediate provisioning while
// background polling remains single-homed in the API server.
func WithDisablePoller() ControllerOption {
	return func(c *SelfHealingController) {
		c.disablePoller = true
	}
}

// SelfHealingController coordinates tenant model provisioning across two complementary paths:
//   - Tier 1 (Event Fast Path): Subscribes to TenantCreated and executes a single immediate
//     reconcile attempt so healthy provisioning finishes in sub-second latency. On transient
//     failure, Reconcile persists status=degraded with the next_retry_at deadline computed from
//     the unified BackoffPolicy and exits immediately—eliminating any split in-memory retry loop.
//   - Tier 2 (Level-Triggered Poller): Scans due pending/degraded records and stale provisioning
//     leases in the database as the single source of truth for deferred retries.
type SelfHealingController struct {
	db              *gorm.DB
	eventBus        *event.EventBus
	reconciler      *TenantReconciler
	dao             *dao.ReconcileDAO
	policy          BackoffPolicy
	pollerInterval  time.Duration
	maxTotalRetries int
	disablePoller   bool

	stopChan    chan struct{}
	stopOnce    sync.Once
	stopMu      sync.RWMutex
	stopped     bool
	unsubscribe func()
	wg          sync.WaitGroup

	// Metrics counters
	tier1SuccessCount int32
	tier1FailureCount int32
	tier2HealedCount  int32
	failedCount       int32
}

// NewSelfHealingController creates a new SelfHealingController.
func NewSelfHealingController(db *gorm.DB, bus *event.EventBus, reconciler *TenantReconciler, opts ...ControllerOption) *SelfHealingController {
	c := &SelfHealingController{
		db:              db,
		eventBus:        bus,
		reconciler:      reconciler,
		dao:             dao.NewReconcileDAO(),
		policy:          DefaultBackoffPolicy,
		pollerInterval:  15 * time.Second,
		maxTotalRetries: 50,
		stopChan:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Start begins Tier-1 event subscription and, unless WithDisablePoller was set,
// the Tier-2 background level-triggered polling loop.
func (c *SelfHealingController) Start(ctx context.Context) {
	// 1. Tier 1: Subscribe to TenantCreated events for single immediate attempt
	if c.eventBus != nil {
		c.unsubscribe = c.eventBus.Subscribe(event.TenantCreated, func(eventCtx context.Context, evt event.TenantLifecycleEvent) error {
			c.stopMu.RLock()
			if c.stopped {
				c.stopMu.RUnlock()
				return nil
			}
			c.wg.Add(1)
			c.stopMu.RUnlock()

			go func() {
				defer c.wg.Done()
				c.handleTier1Event(ctx, evt.TenantID)
			}()
			return nil
		})
	}

	// 2. Tier 2: Level-Triggered Poller loop (skipped in Tier-1-only mode)
	if !c.disablePoller {
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			ticker := time.NewTicker(c.pollerInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-c.stopChan:
					return
				case <-ticker.C:
					c.PollAndHeal(ctx)
				}
			}
		}()
	}
}

// handleTier1Event performs a single immediate provisioning attempt for a newly created tenant.
// If it fails transiently, Reconcile persists status=degraded with next_retry_at computed from
// the unified BackoffPolicy and hands off all subsequent retries to the Tier-2 DB poller.
func (c *SelfHealingController) handleTier1Event(ctx context.Context, tenantID string) {
	select {
	case <-ctx.Done():
		return
	case <-c.stopChan:
		return
	default:
	}

	// Quick verification: ensure tenant & user_tenant are visible
	if err := event.PreFlightCheck(ctx, c.db, tenantID, 50*time.Millisecond); err != nil {
		// Transaction was rolled back or aborted: drop event
		return
	}

	err := c.reconciler.Reconcile(
		ctx,
		c.db,
		tenantID,
		WithEnforceDeadline(),
		WithReconcileBackoffPolicy(c.policy),
	)
	if err == nil {
		state, getErr := c.dao.GetByTenantID(ctx, c.db, tenantID)
		if getErr == nil && state.Status == entity.StatusReady {
			atomic.AddInt32(&c.tier1SuccessCount, 1)
		}
		return
	}

	atomic.AddInt32(&c.tier1FailureCount, 1)
}

// PollAndHeal scans due records in 'pending' or 'degraded' status and triggers self-healing.
func (c *SelfHealingController) PollAndHeal(ctx context.Context) {
	records, err := c.dao.FindDueRetries(ctx, c.db, time.Now(), 50)
	if err != nil || len(records) == 0 {
		return
	}

	for _, rec := range records {
		select {
		case <-ctx.Done():
			return
		case <-c.stopChan:
			return
		default:
		}

		// Re-verify latest record state from DB to prevent racing against concurrent poller replicas
		freshRec, getErr := c.dao.GetByTenantID(ctx, c.db, rec.TenantID)
		if getErr != nil {
			continue
		}

		// Handle stale provisioning recovery: if record has been stuck in provisioning beyond cutoff,
		// recover it to degraded so it becomes immediately eligible for reconciliation.
		if freshRec.Status == entity.StatusProvisioning {
			if freshRec.UpdatedAt.Before(time.Now().Add(-2 * time.Minute)) {
				nextRetry := time.Now()
				_, _ = c.dao.MarkDegraded(ctx, c.db, freshRec.TenantID, freshRec.Version, "recovered stale provisioning lease", nextRetry)
			}
			continue
		}

		if freshRec.Status != entity.StatusPending && freshRec.Status != entity.StatusDegraded {
			continue
		}
		if freshRec.NextRetryAt != nil && freshRec.NextRetryAt.After(time.Now()) {
			continue
		}

		// Check if max total retries exceeded
		if freshRec.RetryCount >= c.maxTotalRetries {
			if _, err := c.dao.MarkFailed(ctx, c.db, freshRec.TenantID, freshRec.Version, "maximum self-healing retries exceeded"); err != nil {
				continue
			}
			atomic.AddInt32(&c.failedCount, 1)
			continue
		}

		// Execute reconciliation (Reconcile handles optimistic locking claim and unified backoff internally)
		if err := c.reconciler.Reconcile(
			ctx,
			c.db,
			freshRec.TenantID,
			WithEnforceDeadline(),
			WithReconcileBackoffPolicy(c.policy),
		); err == nil {
			state, getErr := c.dao.GetByTenantID(ctx, c.db, freshRec.TenantID)
			if getErr == nil && state.Status == entity.StatusReady {
				atomic.AddInt32(&c.tier2HealedCount, 1)
			}
		}
	}
}

// Stop gracefully stops the controller, unsubscribes from events, and drains workers.
func (c *SelfHealingController) Stop() {
	c.stopOnce.Do(func() {
		c.stopMu.Lock()
		c.stopped = true
		c.stopMu.Unlock()

		close(c.stopChan)
		if c.unsubscribe != nil {
			c.unsubscribe()
		}
		c.wg.Wait()
	})
}

// Metrics methods for observability and testing assertions
func (c *SelfHealingController) Tier1SuccessCount() int32 {
	return atomic.LoadInt32(&c.tier1SuccessCount)
}

func (c *SelfHealingController) Tier1FailureCount() int32 {
	return atomic.LoadInt32(&c.tier1FailureCount)
}

func (c *SelfHealingController) Tier2HealedCount() int32 {
	return atomic.LoadInt32(&c.tier2HealedCount)
}

func (c *SelfHealingController) FailedCount() int32 {
	return atomic.LoadInt32(&c.failedCount)
}
