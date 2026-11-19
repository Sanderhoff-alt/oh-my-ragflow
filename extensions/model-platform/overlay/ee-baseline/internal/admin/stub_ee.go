//go:build ee

package admin

import (
	"context"
	"errors"

	"ragflow/internal/common"
	"ragflow/internal/server"
)

// AppendServiceList adds no enterprise service entries.
func AppendServiceList(serviceInfos []*common.BaseMessage) []*common.BaseMessage {
	return serviceInfos
}

// GetEEServicesStatus reports no enterprise services in the community edition.
func (s *Service) GetEEServicesStatus(ctx context.Context) []ServiceStatus { return nil }

// ListUsersEE reports that per-user billing and quota filters are an
// enterprise capability.
func (s *Service) ListUsersEE(ctx context.Context, pageIndex, pageSize int, name string, status, role, sort, orderBy, plan string, top, days int, quota *int) ([]map[string]interface{}, int64, error) {
	return nil, 0, errors.New("listing users with plan and quota filters is only available in the enterprise edition")
}

// UpdateServer records the heartbeat in the server store. The community
// edition has no license state to reconcile, but reporting CodeLicenseValid
// keeps the shared client and admin contract intact: AdminClient.SendHeartbeat
// treats any other code as a failure.
func UpdateServer(serverName string, status *common.BaseMessage) (common.ErrorCode, map[string]interface{}, string) {
	if err := GlobalServerStore.UpdateServerInfo(serverName, status); err != nil {
		return common.CodeErrorServerStatus, nil, err.Error()
	}
	return common.CodeLicenseValid, map[string]interface{}{}, ""
}

// CheckClientStatus accepts every heartbeat: the community edition has no
// license to reconcile.
func CheckClientStatus(serverName string, status *common.BaseMessage) error { return nil }

// InitLicense is a no-op: the community edition has no license.
func InitLicense(ctx context.Context) error { return nil }

// CheckServicePeriodically is a no-op: the community edition has no license
// expiry to watch.
func CheckServicePeriodically() {
	GlobalServerStore.CleanupStaleStatuses(server.GetConfig().GetHeartbeatInterval() * 10)
}
