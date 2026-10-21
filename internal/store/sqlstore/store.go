package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Store is the one implementation of store.Store.
type Store struct {
	db       *sqlx.DB
	d        Dialect
	attempts int
}

var _ store.Store = (*Store)(nil)

// Engine reports the dialect's engine.
func (s *Store) Engine() store.Engine { return s.d.Name() }

// Attempts is the effective TxAttempts (tests).
func (s *Store) Attempts() int { return s.attempts }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

type kind int

const (
	kindTx kind = iota
	kindAppend
	kindRead
)

// Tx runs fn in a serializable transaction (Decision 26).
func (s *Store) Tx(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return s.run(ctx, kindTx, fn)
}

// Append is as Tx, and the only kind in which NextSeq and AppendMessage work.
func (s *Store) Append(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return s.run(ctx, kindAppend, fn)
}

// Read runs fn in a read-only snapshot; it is never retried.
func (s *Store) Read(ctx context.Context, fn func(context.Context, store.Tx) error) error {
	return s.run(ctx, kindRead, fn)
}

func (s *Store) run(ctx context.Context, k kind, fn func(context.Context, store.Tx) error) error {
	attempts := s.attempts
	if k == kindRead {
		attempts = 1
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = s.once(ctx, k, fn)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !s.d.IsRetryable(err) {
			return err
		}
		// Backoff between attempts is Postgres' (Decision 69, Task 3).
	}
	return fmt.Errorf("%w: %v", store.ErrRetryable, err)
}

// once runs one attempt. Every stage's error is returned unclassified: the
// caller decides on retry (BeginTx, LockAppend, fn and Commit alike).
func (s *Store) once(ctx context.Context, k kind, fn func(context.Context, store.Tx) error) error {
	opts := &sql.TxOptions{Isolation: sql.LevelSerializable}
	if k == kindRead {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	sx, err := s.db.BeginTxx(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = sx.Rollback() }()
	if k == kindAppend {
		if err := s.d.LockAppend(ctx, sx); err != nil {
			return err
		}
	}
	t := &tx{tx: sx, d: s.d, append: k == kindAppend, readOnly: k == kindRead}
	if err := fn(ctx, t); err != nil {
		if k == kindRead && s.d.IsReadOnlyViolation(err) {
			return fmt.Errorf("%w: %v", store.ErrReadOnly, err)
		}
		return err
	}
	return sx.Commit()
}
