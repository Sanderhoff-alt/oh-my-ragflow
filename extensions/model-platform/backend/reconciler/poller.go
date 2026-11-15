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

// WithBackoffPolicy sets a custom BackoffPolicy.
func WithBackoffPolicy(policy BackoffPolicy) ControllerOption {
	return func(c *SelfHealingController) {
		c.policy = policy
		if len(policy.Tier1Intervals) > 0 {
			c.maxTier1Retries = len(policy.Tier1Intervals)
		}
	}
}

// WithPollerInterval sets the level-triggered scanning interval.
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

// SelfHealingController coordinates two-tier self-healing for tenant model provisioning:
// - Tier 1 (Fast Queue): Reacts immediately to TenantCreated events with up to 5 fast exponential retries.
// - Tier 2 (Level-Triggered Poller): Continuously scans degraded/pending records to heal from service outages (e.g. 45s gateway maintenance).
type SelfHealingController struct {
	db              *gorm.DB
	eventBus        *event.EventBus
	reconciler      *TenantReconciler
	dao             *dao.ReconcileDAO
	policy          BackoffPolicy
	pollerInterval  time.Duration
	maxTier1Retries int
	maxTotalRetries int

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

	tier2Mu       sync.Mutex
	tier2Attempts map[string]int
}

// NewSelfHealingController creates a new SelfHealingController.
func NewSelfHealingController(db *gorm.DB, bus *event.EventBus, reconciler *TenantReconciler, opts ...ControllerOption) *SelfHealingController {
	c := &SelfHealingController{
		db:              db,
		eventBus:        bus,
		reconciler:      reconciler,
		dao:             dao.NewReconcileDAO(),
		policy:          DefaultBackoffPolicy,
		pollerInterval:  30 * time.Second,
		maxTier1Retries: 5,
		maxTotalRetries: 50,
		stopChan:        make(chan struct{}),
		tier2Attempts:   make(map[string]int),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Start begins event subscription (Tier 1) and background level-triggered polling (Tier 2).
func (c *SelfHealingController) Start(ctx context.Context) {
	// 1. Tier 1: Subscribe to TenantCreated events
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

	// 2. Tier 2: Level-Triggered Poller loop
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

// handleTier1Event processes a newly created tenant with fast exponential retries.
func (c *SelfHealingController) handleTier1Event(ctx context.Context, tenantID string) {
	// A. Quick verification: ensure tenant & user_tenant are visible
	if err := event.PreFlightCheck(ctx, c.db, tenantID, 50*time.Millisecond); err != nil {
		// Transaction was rolled back or aborted: drop event
		return
	}

	// B. Fast retry loop (up to maxTier1Retries)
	for attempt := 0; attempt < c.maxTier1Retries; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-c.stopChan:
			return
		default:
		}

		err := c.reconciler.Reconcile(ctx, c.db, tenantID)
		if err == nil {
			state, getErr := c.dao.GetByTenantID(ctx, c.db, tenantID)
			if getErr == nil && state.Status == entity.StatusReady {
				atomic.AddInt32(&c.tier1SuccessCount, 1)
			}
			return
		}

		delay := c.policy.CalculateTier1Backoff(attempt)
		select {
		case <-ctx.Done():
			return
		case <-c.stopChan:
			return
		case <-time.After(delay):
		}
	}

	// C. Tier 1 exhausted: schedule first Tier 2 slow retry without double-incrementing retry count
	atomic.AddInt32(&c.tier1FailureCount, 1)
	rec, err := c.dao.GetByTenantID(ctx, c.db, tenantID)
	if err == nil && rec.Status == entity.StatusDegraded {
		c.tier2Mu.Lock()
		c.tier2Attempts[tenantID] = 1
		c.tier2Mu.Unlock()

		nextRetry := time.Now().Add(c.policy.CalculateTier2Backoff(0))
		_ = c.db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
			Where("tenant_id = ? AND version = ?", tenantID, rec.Version).
			Update("next_retry_at", nextRetry).Error
	}
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
			c.tier2Mu.Lock()
			delete(c.tier2Attempts, freshRec.TenantID)
			c.tier2Mu.Unlock()
			continue
		}

		// Execute reconciliation (Reconcile handles optimistic locking claim internally with deadline enforcement)
		if err := c.reconciler.Reconcile(ctx, c.db, freshRec.TenantID, WithEnforceDeadline()); err == nil {
			state, getErr := c.dao.GetByTenantID(ctx, c.db, freshRec.TenantID)
			if getErr == nil && state.Status == entity.StatusReady {
				atomic.AddInt32(&c.tier2HealedCount, 1)
				c.tier2Mu.Lock()
				delete(c.tier2Attempts, freshRec.TenantID)
				c.tier2Mu.Unlock()
			}
		} else {
			// Reconcile internally marked degraded with Tier-1 interval.
			// In Tier-2 level-triggered poller, update next_retry_at to Tier-2 backoff schedule.
			latestRec, getErr := c.dao.GetByTenantID(ctx, c.db, freshRec.TenantID)
			if getErr == nil && latestRec.Status == entity.StatusDegraded {
				c.tier2Mu.Lock()
				attempt, exists := c.tier2Attempts[freshRec.TenantID]
				if !exists {
					if freshRec.Status == entity.StatusPending {
						attempt = 0
					} else if latestRec.RetryCount > c.maxTier1Retries {
						attempt = latestRec.RetryCount - c.maxTier1Retries
					} else if latestRec.RetryCount > 0 {
						attempt = latestRec.RetryCount - 1
					} else {
						attempt = 0
					}
				}
				c.tier2Attempts[freshRec.TenantID] = attempt + 1
				c.tier2Mu.Unlock()

				nextRetry := time.Now().Add(c.policy.CalculateTier2Backoff(attempt))
				_ = c.db.WithContext(ctx).Model(&entity.TenantReconcileRecord{}).
					Where("tenant_id = ? AND version = ?", freshRec.TenantID, latestRec.Version).
					Update("next_retry_at", nextRetry).Error
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
