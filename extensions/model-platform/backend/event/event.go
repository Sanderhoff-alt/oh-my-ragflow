package event

import (
	"fmt"
	"time"
)

// EventType defines the type of tenant lifecycle events.
type EventType string

const (
	// TenantCreated indicates a new tenant has been committed and needs model platform provisioning.
	TenantCreated EventType = "tenant.created"
	// TenantReconcileRequest indicates an explicit reconcile request (e.g. from background poller).
	TenantReconcileRequest EventType = "tenant.reconcile"
)

// TenantLifecycleEvent represents an asynchronous event emitted across tenant lifecycle stages.
type TenantLifecycleEvent struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	TenantID  string    `json:"tenant_id"`
	Timestamp time.Time `json:"timestamp"`
}

// NewTenantCreatedEvent constructs a new TenantCreated event.
func NewTenantCreatedEvent(tenantID string) TenantLifecycleEvent {
	return TenantLifecycleEvent{
		ID:        fmt.Sprintf("evt-created-%s-%d", tenantID, time.Now().UnixNano()),
		Type:      TenantCreated,
		TenantID:  tenantID,
		Timestamp: time.Now(),
	}
}

// NewTenantReconcileEvent constructs a new TenantReconcileRequest event.
func NewTenantReconcileEvent(tenantID string) TenantLifecycleEvent {
	return TenantLifecycleEvent{
		ID:        fmt.Sprintf("evt-reconcile-%s-%d", tenantID, time.Now().UnixNano()),
		Type:      TenantReconcileRequest,
		TenantID:  tenantID,
		Timestamp: time.Now(),
	}
}
