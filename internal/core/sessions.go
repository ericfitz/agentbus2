package core

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/store"
)

// IsLive reports whether the session is open and its lease, plus the skew,
// has not passed (plan choice 6, Decision 49). A closed session is never live.
func (c *Core) IsLive(s store.Session, nowMs int64) bool {
	return s.ClosedAt == 0 && s.LeaseExpiresAt+int64(c.Cfg.LeaseSkewSeconds)*1000 >= nowMs
}

// LiveSessions lists the identity's live sessions at the core's clock.
func (c *Core) LiveSessions(ctx context.Context, tx store.Tx, identity string) ([]store.Session, error) {
	all, err := tx.ListSessions(ctx, identity)
	if err != nil {
		return nil, FromStore(err)
	}
	now := c.NowMs()
	var live []store.Session
	for _, s := range all {
		if c.IsLive(s, now) {
			live = append(live, s)
		}
	}
	return live, nil
}

// LeaseUntil is the lease expiry for a lease taken at nowMs.
func (c *Core) LeaseUntil(nowMs int64) int64 {
	return nowMs + int64(c.Cfg.SessionLeaseSeconds)*1000
}
