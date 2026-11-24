package reconciler

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"ragflow/extensions/model-platform/backend/gateway"

	"gorm.io/gorm"
)

// flakyCommitPool makes the transaction that updates the managed instance report a failed commit although
// the commit went through, like a connection that drops right after the server acknowledged it.
type flakyCommitPool struct{ gorm.ConnPool }

func (p *flakyCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.ConnPool.(gorm.TxBeginner).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &flakyTx{Tx: tx}, nil
}

type flakyTx struct {
	*sql.Tx
	touchedInstance bool
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
