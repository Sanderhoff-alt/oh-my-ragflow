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

// StartServer starts the model platform background workers in the API server process.
func StartServer(ctx context.Context, cancel context.CancelFunc, serverName string, serverType common.ServerType) error {
	if serverType != common.ServerTypeAPI {
		return nil
	}
	w, err := mpworker.Start(ctx)
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
