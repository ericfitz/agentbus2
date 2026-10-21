package sessions

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Close ends the identity's own session (v1 EndSessions). It does not go
// through Require: a lapsed row may be closed, which is how a client frees a
// crashed session's display name. Closing sets the lease to now so the
// retention sweep removes the row; closing a closed row is a no-op.
func (s Service) Close(ctx context.Context, identity string, in bus.CloseSessionInput) error {
	if in.Session == "" {
		return core.E(bus.CodeValidation, "session is required")
	}
	payload := struct{ Session string }{in.Session}
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		var res struct{}
		_, err := s.WithReceipt(ctx, tx, identity, in.IdempotencyKey, payload, &res, func() error {
			row, err := tx.Session(ctx, in.Session)
			if err != nil {
				return core.FromStore(err)
			}
			if row.Identity != identity {
				return core.E(bus.CodeNotFound, "session %q does not exist", in.Session)
			}
			if row.ClosedAt != 0 {
				return nil
			}
			now := s.NowMs()
			if err := tx.CloseSession(ctx, row.ID, now); err != nil {
				return core.FromStore(err)
			}
			return core.FromStore(tx.RenewLease(ctx, row.ID, now))
		})
		return err
	})
	return core.FromStore(err)
}
