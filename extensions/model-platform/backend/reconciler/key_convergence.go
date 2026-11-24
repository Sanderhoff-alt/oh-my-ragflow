package reconciler

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoCurrentKey is returned when a convergence is asked to keep a key but no key hash is known.
// Converging without a hash would revoke the tenant's only working credential.
var ErrNoCurrentKey = errors.New("tenant has no current key hash to converge on")

// ConvergeTenantKeys makes the gateway hold exactly one virtual key for the tenant: the one whose
// sha256 digest is keepHash. Every other key of the tenant's gateway team is revoked, and the number
// of revoked keys is returned.
//
// The gateway team id is the tenant id and the team only ever holds platform-issued keys, so the team
// is the stable identity of a tenant's credentials, unlike key aliases (not reliably unique) or raw
// tokens (not stored by the platform). The operation is idempotent and level-triggered: it can be
// repeated after any partial failure and always ends in the same state.
//
// Callers must hold the tenant instance lock, otherwise a key that is being issued for a concurrent
// rotation would be revoked before it is recorded.
func ConvergeTenantKeys(ctx context.Context, client GatewayKeyClient, tenantID, keepHash string) (int, error) {
	if keepHash == "" {
		return 0, ErrNoCurrentKey
	}
	return revokeTeamKeysExcept(ctx, client, tenantID, keepHash)
}

// RevokeTenantKeys revokes every virtual key of the tenant's gateway team.
func RevokeTenantKeys(ctx context.Context, client GatewayKeyClient, tenantID string) (int, error) {
	return revokeTeamKeysExcept(ctx, client, tenantID, "")
}

func revokeTeamKeysExcept(ctx context.Context, client GatewayKeyClient, tenantID, keepHash string) (int, error) {
	keys, err := client.ListTeamKeys(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("failed to list the gateway keys of tenant %s: %w", tenantID, err)
	}

	var stale []string
	for _, k := range keys {
		if k.Token != "" && k.Token != keepHash {
			stale = append(stale, k.Token)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}
	if err := client.DeleteKeysByHash(ctx, stale); err != nil {
		return 0, fmt.Errorf("failed to revoke %d gateway key(s) of tenant %s: %w", len(stale), tenantID, err)
	}
	return len(stale), nil
}
