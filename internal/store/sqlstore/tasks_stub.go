// Stubs for the tasks aggregate; replaced by Task 9.

package sqlstore

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

func (t *tx) LiveMemories(context.Context, int64) ([]store.Message, error) {
	return nil, ErrNotImplemented
}
func (t *tx) ChannelLastActivity(context.Context, int64) (int64, error) {
	return 0, ErrNotImplemented
}
