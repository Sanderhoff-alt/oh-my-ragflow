package event

import (
	"sync"

	"gorm.io/gorm"
)

type postCommitWrapper struct {
	gorm.ConnPool
	committer gorm.TxCommitter
	hooks     []func()
	mu        sync.Mutex
}

func (w *postCommitWrapper) Commit() error {
	err := w.committer.Commit()
	if err == nil {
		w.mu.Lock()
		hooks := append([]func(){}, w.hooks...)
		w.hooks = nil
		w.mu.Unlock()
		for _, h := range hooks {
			if h != nil {
				h()
			}
		}
	}
	return err
}

func (w *postCommitWrapper) Rollback() error {
	w.mu.Lock()
	w.hooks = nil
	w.mu.Unlock()
	return w.committer.Rollback()
}

// RegisterPostCommitHook registers a callback to be executed strictly after the current
// transaction has successfully committed to the database.
// If the transaction is rolled back or aborts, the registered hook is cancelled and never invoked.
// If tx is not an active transaction (autocommit mode), the hook is executed immediately.
func RegisterPostCommitHook(tx *gorm.DB, hook func()) {
	if tx == nil || hook == nil {
		return
	}
	if tx.Statement == nil || tx.Statement.ConnPool == nil {
		hook()
		return
	}

	if existing, ok := tx.Statement.ConnPool.(*postCommitWrapper); ok {
		existing.mu.Lock()
		existing.hooks = append(existing.hooks, hook)
		existing.mu.Unlock()
		return
	}

	if committer, ok := tx.Statement.ConnPool.(gorm.TxCommitter); ok {
		wrapper := &postCommitWrapper{
			ConnPool:  tx.Statement.ConnPool,
			committer: committer,
			hooks:     []func(){hook},
		}
		tx.Statement.ConnPool = wrapper
		return
	}

	// Autocommit mode
	hook()
}
