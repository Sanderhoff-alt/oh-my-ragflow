//go:build ee

package clickhouse

import "ragflow/internal/common"

// Driver is the analytics sink. The OSS build never opens a ClickHouse
// connection, so every method drops what it is given.
type Driver struct{}

// GetDriver returns the analytics sink, which the OSS build never creates.
func GetDriver() *Driver { return nil }

// CollectModelUsage records token usage for analytics.
func (d *Driver) CollectModelUsage(modelUsage *common.ModelUsage) error { return nil }

// SaveOperationLog records an operation log entry.
func (d *Driver) SaveOperationLog(operationLog *common.OperationLog) error { return nil }

// Status reports the analytics backend health.
func (d *Driver) Status() (map[string]interface{}, error) { return nil, nil }
