// This file is replaced by Task 5: sessions: open, close, identities.

package local

import (
	"context"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
)

func (b *Bus) Open(ctx context.Context, in ab2bus.OpenSessionInput) (ab2bus.Session, error) {
	return ab2bus.Session{}, ErrNotImplemented
}

func (b *Bus) Close(ctx context.Context, in ab2bus.CloseSessionInput) error {
	return ErrNotImplemented
}

func (b *Bus) Identities(ctx context.Context) ([]ab2bus.IdentityInfo, error) {
	return nil, ErrNotImplemented
}
