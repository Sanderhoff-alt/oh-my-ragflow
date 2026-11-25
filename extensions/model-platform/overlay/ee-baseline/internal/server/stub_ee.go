//go:build ee

package server

import (
	"context"
	"errors"

	mpworker "ragflow/extensions/model-platform/backend/worker"
	"ragflow/internal/common"

	"go.uber.org/zap"
)

var workers *mpworker.Workers

// StartServer starts the model platform background workers for the server process:
//   - ServerTypeAPI runs the full worker set (Tier-1 event fast path, Tier-2 poller,
//     metering daemon, catalog publish worker, orphan cleanup, and dirty key sweep).
//   - ServerTypeAdmin runs only the lightweight Tier-1 TenantCreated listener so
//     admin-created tenants (e.g., POST /api/v1/admin/users) are provisioned
//     immediately while periodic loops stay single-homed in the API server.
func StartServer(ctx context.Context, cancel context.CancelFunc, serverName string, serverType common.ServerType) error {
	var (
		w   *mpworker.Workers
		err error
	)
	switch serverType {
	case common.ServerTypeAPI:
		w, err = mpworker.Start(ctx)
	case common.ServerTypeAdmin:
		w, err = mpworker.StartAdminTier1(ctx)
	default:
		return nil
	}
	if errors.Is(err, mpworker.ErrGatewayNotConfigured) {
		common.Warn("Model platform workers not started", zap.Error(err))
		return nil
	}
	if err != nil {
		return err
	}
	workers = w
	return nil
}

// ShutdownServer stops the model platform background workers.
func ShutdownServer(ctx context.Context) error {
	if workers != nil {
		workers.Stop()
		workers = nil
	}
	return nil
}
