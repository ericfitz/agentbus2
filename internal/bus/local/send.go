package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/messaging"
)

// Send ports v1 internal/bus/messages.go Send through messaging.Service.
func (b *Bus) Send(ctx context.Context, in ab2bus.SendInput) (ab2bus.SendResult, error) {
	return messaging.Service{Core: b.c}.Send(ctx, b.identity, in)
}

// History ports v1 internal/bus/messages.go History through messaging.Service.
func (b *Bus) History(ctx context.Context, in ab2bus.HistoryInput) ([]ab2bus.Message, error) {
	return messaging.Service{Core: b.c}.History(ctx, b.identity, in)
}
