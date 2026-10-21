// This file is replaced by Task 10: receive and watch.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Receive(ctx context.Context, in ab2bus.ReceiveInput) (ab2bus.ReceiveResult, error) {
	return ab2bus.ReceiveResult{}, ErrNotImplemented
}

func (b *Bus) Watch(ctx context.Context, in ab2bus.WatchInput) (ab2bus.WatchResult, error) {
	return ab2bus.WatchResult{}, ErrNotImplemented
}
