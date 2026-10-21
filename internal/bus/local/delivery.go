package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/delivery"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
)

func (b *Bus) delivery() delivery.Service {
	return delivery.Service{Core: b.c, Sessions: sessions.Service{Core: b.c}}
}

// Receive ports v1 internal/bus/receive.go Receive through delivery.Service.
func (b *Bus) Receive(ctx context.Context, in ab2bus.ReceiveInput) (ab2bus.ReceiveResult, error) {
	return b.delivery().Receive(ctx, b.identity, in)
}

// Watch is delivery.Service.Watch as b's identity.
func (b *Bus) Watch(ctx context.Context, in ab2bus.WatchInput) (ab2bus.WatchResult, error) {
	return b.delivery().Watch(ctx, b.identity, in)
}
