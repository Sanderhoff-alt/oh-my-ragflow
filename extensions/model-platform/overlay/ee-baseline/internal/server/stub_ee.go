//go:build ee

package server

import (
	"context"

	"ragflow/internal/common"
)

// StartServer starts the enterprise services attached to a server process.
// The community edition runs none of them.
func StartServer(ctx context.Context, cancel context.CancelFunc, serverName string, serverType common.ServerType) error {
	return nil
}

// ShutdownServer stops the enterprise services attached to a server process.
// The community edition runs none of them.
func ShutdownServer(ctx context.Context) error { return nil }
