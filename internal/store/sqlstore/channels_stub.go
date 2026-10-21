// Stubs for the channels aggregate; replaced by Task 5.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) CreateChannel(context.Context, store.ChannelCreate) (store.Channel, error) {
	return store.Channel{}, ErrNotImplemented
}
func (t *tx) Channel(context.Context, int64) (store.Channel, error) {
	return store.Channel{}, ErrNotImplemented
}
func (t *tx) ChannelByName(context.Context, string) (store.Channel, error) {
	return store.Channel{}, ErrNotImplemented
}
func (t *tx) ListChannels(context.Context) ([]store.Channel, error) { return nil, ErrNotImplemented }
func (t *tx) ListChannelsByKind(context.Context, store.Kind) ([]store.Channel, error) {
	return nil, ErrNotImplemented
}
func (t *tx) RenameChannel(context.Context, int64, string) error    { return ErrNotImplemented }
func (t *tx) DeleteChannel(context.Context, int64) error            { return ErrNotImplemented }
func (t *tx) SetEvictedBefore(context.Context, int64, int64) error  { return ErrNotImplemented }
func (t *tx) ACL(context.Context, int64) ([]store.ACLEntry, error)  { return nil, ErrNotImplemented }
func (t *tx) SetACL(context.Context, int64, []store.ACLEntry) error { return ErrNotImplemented }
func (t *tx) EffectivePermissions(context.Context, int64, string) (store.Permissions, error) {
	return store.Permissions{}, ErrNotImplemented
}
func (t *tx) ReadableChannels(context.Context, string) ([]int64, error) {
	return nil, ErrNotImplemented
}
