// This file is replaced by Task 6: send and history.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Send(ctx context.Context, in ab2bus.SendInput) (ab2bus.SendResult, error) {
	return ab2bus.SendResult{}, ErrNotImplemented
}

func (b *Bus) History(ctx context.Context, in ab2bus.HistoryInput) ([]ab2bus.Message, error) {
	return nil, ErrNotImplemented
}
