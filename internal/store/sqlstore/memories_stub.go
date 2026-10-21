// Stubs for the memories aggregate; replaced by Task 9.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) LiveMemory(context.Context, int64) (store.Message, error) {
	return store.Message{}, ErrNotImplemented
}
func (t *tx) MemoryRevisions(context.Context, int64) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) SetMemory(context.Context, int64, int64, int64) error    { return ErrNotImplemented }
func (t *tx) TouchMemoryAccess(context.Context, []int64, int64) error { return ErrNotImplemented }
func (t *tx) DeleteMemoryAccess(context.Context, int64) error         { return ErrNotImplemented }
func (t *tx) ExpiredMemories(context.Context, int64, int) ([]store.MemoryRef, error) {
	return nil, ErrNotImplemented
}
