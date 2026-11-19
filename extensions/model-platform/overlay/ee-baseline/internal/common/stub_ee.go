//go:build ee

package common

import (
	"time"

	"go.uber.org/zap"
)

var (
	OpenSourceVersion = "open source"
	EnterpriseEdition = "enterprise edition"
)

// GetRAGFlowType reports the edition this binary was built for.
func GetRAGFlowType() string {
	return OpenSourceVersion
}

func InitAudit(logType string, file FileOutput, serviceName string) error {
	return nil
}

type OperationLog struct {
	EventTime     time.Time      `json:"event_time"`
	UserID        string         `json:"user_id"`
	IPAddress     string         `json:"ip_address"`
	Operation     string         `json:"operation"`
	APIPath       string         `json:"api_path"`
	HTTPMethod    string         `json:"http_method"`
	RequestParams string         `json:"request_params"`
	Resources     map[string]any `json:"resources"`
	Code          int            `json:"code"`
	Error         error          `json:"error"`
	DurationMS    int64          `json:"duration_ms"`
}

func Audit(operationLog *OperationLog, fields ...zap.Field) {
}

// SyncAudit is the OSS no-op flush for the audit logger.
func SyncAudit() {
}

// InitIngestLog is the OSS no-op hook for ingestion lifecycle event sinks.
func InitIngestLog(logType string, file FileOutput, serviceName string) error {
	return nil
}

// SyncIngestLog is the OSS no-op flush for the ingest logger.
func SyncIngestLog() {
}

// LogIngest is the OSS no-op implementation of the shared upstream seam.
func LogIngest(log *IngestionLog, fields ...zap.Field) {}
