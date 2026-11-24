package worker

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"ragflow/extensions/model-platform/backend/event"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/metering"
	"ragflow/extensions/model-platform/backend/publisher"
	"ragflow/extensions/model-platform/backend/reconciler"

	"gorm.io/gorm"
)

var (
	// ErrGatewayNotConfigured indicates the LiteLLM gateway endpoint or master key is missing.
	ErrGatewayNotConfigured = errors.New("model platform gateway is not configured: LITELLM_BASE_URL and LITELLM_MASTER_KEY must be set")
	// ErrNoDatabase indicates no database handle is available for the background workers.
	ErrNoDatabase = errors.New("model platform workers require a database handle")
)

var dbProvider func() *gorm.DB

const (
	orphanScanInterval = time.Hour

	keySweepInterval = time.Minute
	keySweepMinAge   = 2 * time.Minute
	keySweepBatch    = 50
)

// SetDBProvider registers how the workers obtain the process database handle.
// The upstream server package cannot import its dao package, so the dao seam registers it.
func SetDBProvider(provider func() *gorm.DB) {
	dbProvider = provider
}

// Workers owns the model platform background loops of one server process.
type Workers struct {
	controller *reconciler.SelfHealingController
	daemon     *metering.MeteringDaemon
	publisher  *publisher.Worker
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

// Start launches the background workers. Without a configured gateway nothing is started,
// pending tenants stay pending, and ErrGatewayNotConfigured is returned.
func Start(ctx context.Context) (*Workers, error) {
	baseURL := os.Getenv("LITELLM_BASE_URL")
	masterKey := os.Getenv("LITELLM_MASTER_KEY")
	if baseURL == "" || masterKey == "" {
		return nil, ErrGatewayNotConfigured
	}
	if dbProvider == nil {
		return nil, ErrNoDatabase
	}
	db := dbProvider()
	if db == nil {
		return nil, ErrNoDatabase
	}

	client := gateway.NewClient(gateway.GatewayConfig{BaseURL: baseURL, MasterKey: masterKey})
	tenantReconciler := reconciler.NewTenantReconciler(client, baseURL, nil)
	controller := reconciler.NewSelfHealingController(db, event.GetDefaultBus(), tenantReconciler)
	controller.Start(ctx)

	daemon := metering.NewMeteringDaemon(metering.NewSpendLogCollector(db, client), metering.NewUsagePipeline(db))
	daemon.Start(ctx)

	// The publish worker converges tenants toward the managed model catalog. Several replicas can run it
	// because every item is claimed with an optimistic lock.
	publishWorker := publisher.NewWorker(func() *gorm.DB { return db }, client)
	publishWorker.Start(ctx)

	scanCtx, cancel := context.WithCancel(ctx)
	w := &Workers{controller: controller, daemon: daemon, publisher: publishWorker, cancel: cancel}
	w.wg.Add(2)
	go w.scanOrphanedTenants(scanCtx, db, reconciler.NewTenantDecommissioner(client))
	go w.sweepDirtyKeys(scanCtx, db, reconciler.NewKeyRotator(client, nil))

	return w, nil
}

// sweepDirtyKeys finishes key rotations that were interrupted or could not revoke the superseded keys.
// A tenant is picked up once its rotation has been unfinished for keySweepMinAge, so a rotation that is still
// running is left alone. The sweep is idempotent, and failures are retried on the next tick.
func (w *Workers) sweepDirtyKeys(ctx context.Context, db *gorm.DB, rotator *reconciler.KeyRotator) {
	defer w.wg.Done()
	ticker := time.NewTicker(keySweepInterval)
	defer ticker.Stop()
	for {
		_, _ = rotator.ConvergeDirtyTenants(ctx, db, keySweepMinAge, keySweepBatch)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scanOrphanedTenants revokes the gateway keys of tenants removed from the upstream tenant table.
// The scan is idempotent, and failures are retried on the next tick.
func (w *Workers) scanOrphanedTenants(ctx context.Context, db *gorm.DB, decommissioner *reconciler.TenantDecommissioner) {
	defer w.wg.Done()
	ticker := time.NewTicker(orphanScanInterval)
	defer ticker.Stop()
	for {
		_, _ = decommissioner.ScanAndDecommissionOrphanedTenants(ctx, db)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Stop terminates the background workers and waits for in-flight work to finish.
func (w *Workers) Stop() {
	w.cancel()
	w.wg.Wait()
	w.publisher.Stop()
	w.daemon.Stop()
	w.controller.Stop()
}
