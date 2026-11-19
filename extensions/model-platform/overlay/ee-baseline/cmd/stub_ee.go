//go:build ee

package main

import (
	"context"
	"errors"

	"ragflow/internal/server/bootstrap"
)

// runDeepDoc reports that the EE DeepDoc inference server is an enterprise
// capability.
func runDeepDoc(ctx context.Context, serverName string, args *bootstrap.ServerArgs) error {
	return errors.New("the DeepDoc inference server is only available in the enterprise edition")
}
