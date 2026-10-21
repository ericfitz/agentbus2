// Stubs for the sessions aggregate; replaced by Task 7.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) OpenSession(context.Context, store.Session) error { return ErrNotImplemented }
func (t *tx) Session(context.Context, string) (store.Session, error) {
	return store.Session{}, ErrNotImplemented
}
func (t *tx) SessionByResumeTokenHash(context.Context, string) (store.Session, error) {
	return store.Session{}, ErrNotImplemented
}
func (t *tx) ListSessions(context.Context, string) ([]store.Session, error) {
	return nil, ErrNotImplemented
}
func (t *tx) RenewLease(context.Context, string, int64) error            { return ErrNotImplemented }
func (t *tx) CloseSession(context.Context, string, int64) error          { return ErrNotImplemented }
func (t *tx) DeleteSessions(context.Context, int64, int) (int, error)    { return 0, ErrNotImplemented }
func (t *tx) RegisterWatch(context.Context, string, string, int64) error { return ErrNotImplemented }
func (t *tx) SetPendingToken(context.Context, string, string) error      { return ErrNotImplemented }
func (t *tx) SetTagsCursor(context.Context, string, int64, int64) error  { return ErrNotImplemented }
func (t *tx) Cursors(context.Context, string) ([]store.Cursor, error)    { return nil, ErrNotImplemented }
func (t *tx) PutCursor(context.Context, store.Cursor) error              { return ErrNotImplemented }
func (t *tx) DeleteCursor(context.Context, string, int64) error          { return ErrNotImplemented }
