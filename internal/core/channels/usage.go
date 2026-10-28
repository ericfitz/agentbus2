package channels

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Usage is the administrator's usage reading (Decision 48), the three members
// of v1 internal/bus/admin.go Status (UsageBytes, BudgetBytes, Notice) as
// their own operation. The admin check and the capacity notice are one Store
// transaction; Store.Usage runs outside it (a PRAGMA on SQLite).
func (s Service) Usage(ctx context.Context, identity string) (bus.Usage, error) {
	var out bus.Usage
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		out = bus.Usage{}
		if err := s.RequireServerAdmin(ctx, tx, identity); err != nil {
			return err
		}
		n, ok, err := tx.Notice(ctx, noticeCapacity)
		if err != nil {
			return err
		}
		if ok {
			out.Notice = n.Message
		}
		return nil
	})
	if err != nil {
		return bus.Usage{}, err
	}
	u, err := s.Store.Usage(ctx)
	if err != nil {
		return bus.Usage{}, err
	}
	out.Bytes, out.Evictable = u.Bytes, !u.Logical // interim until the Postgres budget task
	out.BudgetBytes = int64(s.Cfg.SQLiteBudgetMiB) << 20
	return out, nil
}
