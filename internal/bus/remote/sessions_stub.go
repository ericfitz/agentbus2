package remote

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Open(_ context.Context, _ ab2bus.OpenSessionInput) (ab2bus.Session, error) {
	return ab2bus.Session{}, notImplemented("Open")
}

func (b *Bus) Close(_ context.Context, _ ab2bus.CloseSessionInput) error {
	return notImplemented("Close")
}

func (b *Bus) Receive(_ context.Context, _ ab2bus.ReceiveInput) (ab2bus.ReceiveResult, error) {
	return ab2bus.ReceiveResult{}, notImplemented("Receive")
}

func (b *Bus) Watch(_ context.Context, _ ab2bus.WatchInput) (ab2bus.WatchResult, error) {
	return ab2bus.WatchResult{}, notImplemented("Watch")
}

func (b *Bus) Identities(_ context.Context) ([]ab2bus.IdentityInfo, error) {
	return nil, notImplemented("Identities")
}
