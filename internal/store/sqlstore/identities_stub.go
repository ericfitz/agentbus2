// Stubs for the identities aggregate; replaced by Task 4.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) CreateIdentity(context.Context, string, int64) error { return ErrNotImplemented }
func (t *tx) Identity(context.Context, string) (store.Identity, error) {
	return store.Identity{}, ErrNotImplemented
}
func (t *tx) ListIdentities(context.Context) ([]store.Identity, error) { return nil, ErrNotImplemented }
func (t *tx) SetAdmin(context.Context, string, bool) error             { return ErrNotImplemented }
func (t *tx) AdvanceTagAckedMark(context.Context, string, int64) error { return ErrNotImplemented }
func (t *tx) PutPublicKey(context.Context, store.PublicKey) error      { return ErrNotImplemented }
func (t *tx) PublicKeys(context.Context, string) ([]store.PublicKey, error) {
	return nil, ErrNotImplemented
}
func (t *tx) DeletePublicKeys(context.Context, string) (int, error) { return 0, ErrNotImplemented }
