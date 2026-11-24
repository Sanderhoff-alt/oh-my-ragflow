package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"ragflow/extensions/model-platform/backend/dao"
	"ragflow/extensions/model-platform/backend/gateway"

	"gorm.io/gorm"
)

// flakyCommitPool makes the transaction that updates the managed instance report a failed commit although
// the commit went through, like a connection that drops right after the server acknowledged it.
type flakyCommitPool struct {
	gorm.ConnPool
	afterCommit func()
}

func (p *flakyCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &flakyTx{Tx: tx, afterCommit: p.afterCommit}, nil
}

type flakyTx struct {
	*sql.Tx
	touchedInstance bool
	afterCommit     func()
}

func (t *flakyTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if strings.Contains(query, "tenant_model_instance") {
		t.touchedInstance = true
	}
	return t.Tx.ExecContext(ctx, query, args...)
}

func (t *flakyTx) Commit() error {
	err := t.Tx.Commit()
	if err == nil && t.touchedInstance {
		if t.afterCommit != nil {
			t.afterCommit()
		}
		return errors.New("connection reset by peer")
	}
	return err
}

func TestKeyRotator_CommitThatReportsAnErrorKeepsTheNewKey(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-commit-ack-lost"
	oldKey := provisionReadyTenant(t, db, m, tenantID)

	db.ConnPool = &flakyCommitPool{ConnPool: db.ConnPool}
	db.Statement.ConnPool = db.ConnPool

	res, err := NewKeyRotator(m, nil).Rotate(context.Background(), db, tenantID, "admin")
	if err != nil {
		t.Fatalf("a rotation that committed must not fail: %v", err)
	}
	newKey := instanceKey(t, db, tenantID)
	if newKey == oldKey || res.KeyHash != gateway.HashKey(newKey) {
		t.Fatalf("expected the committed new key, got %+v", res)
	}
	if _, ok := m.generated[newKey]; !ok {
		t.Fatal("the key the tenant now uses must not be revoked")
	}
	if _, ok := m.generated[oldKey]; ok {
		t.Error("the superseded key must be revoked")
	}
}

// When the connection is gone, the read that confirms the commit fails as well. That proves nothing about the
// commit, so the new key and the dirty marker must stay for the sweeper, which converges on what the record names.
func TestKeyRotator_UnconfirmedCommitLeavesTheOutcomeToTheSweeper(t *testing.T) {
	db := setupTestDB(t)
	m := newMockGatewayClient()
	tenantID := "tenant-rotate-commit-unconfirmed"
	oldKey := provisionReadyTenant(t, db, m, tenantID)

	var readsFail atomic.Bool
	if err := db.Callback().Query().Before("gorm:query").Register("test:fail_record_reads", func(tx *gorm.DB) {
		if readsFail.Load() && tx.Statement.Table == "platform_tenant_reconcile" {
			_ = tx.AddError(errors.New("connection refused"))
		}
	}); err != nil {
		t.Fatalf("failed to register the fault: %v", err)
	}
	db.ConnPool = &flakyCommitPool{ConnPool: db.ConnPool, afterCommit: func() { readsFail.Store(true) }}
	db.Statement.ConnPool = db.ConnPool

	rotator := NewKeyRotator(m, nil)
	if _, err := rotator.Rotate(context.Background(), db, tenantID, "admin"); err == nil {
		t.Fatal("a rotation whose outcome is unknown must report an error")
	}
	readsFail.Store(false)

	newKey := instanceKey(t, db, tenantID)
	if newKey == oldKey {
		t.Fatal("the commit went through, the instance must hold the new key")
	}
	if _, ok := m.generated[newKey]; !ok {
		t.Fatal("the key the tenant now uses must not be revoked while the outcome is unknown")
	}
	rec, _ := dao.NewReconcileDAO().GetByTenantID(context.Background(), db, tenantID)
	if rec.KeyDirtySince == nil || rec.KeyHash != gateway.HashKey(newKey) {
		t.Fatalf("the marker must stay and the record must name the new key: %+v", rec)
	}

	if n, err := rotator.ConvergeDirtyTenants(context.Background(), db, 0, 10); err != nil || n != 1 {
		t.Fatalf("expected the sweep to converge 1 tenant, got %d (err=%v)", n, err)
	}
	if _, ok := m.generated[oldKey]; ok {
		t.Error("the sweep must revoke the superseded key")
	}
}
