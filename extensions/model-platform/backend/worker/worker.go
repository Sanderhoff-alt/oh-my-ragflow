package worker

import (
	"context"
	"errors"
	"os"

	"ragflow/extensions/model-platform/backend/event"
	"ragflow/extensions/model-platform/backend/gateway"
	"ragflow/extensions/model-platform/backend/metering"
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

// SetDBProvider registers how the workers obtain the process database handle.
// The upstream server package cannot import its dao package, so the dao seam registers it.
func SetDBProvider(provider func() *gorm.DB) {
	dbProvider = provider
}

// Workers owns the model platform background loops of one server process.
type Workers struct {
	controller *reconciler.SelfHealingController
	daemon     *metering.MeteringDaemon
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

	daemon := metering.NewMeteringDaemon(db, metering.NewSpendLogCollector(db, client), metering.NewUsagePipeline(db))
	daemon.Start(ctx)

	return &Workers{controller: controller, daemon: daemon}, nil
}

// Stop terminates the background workers and waits for in-flight work to finish.
func (w *Workers) Stop() {
	w.daemon.Stop()
	w.controller.Stop()
}
