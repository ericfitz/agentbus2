package channels

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Usage is the administrator's usage reading (Decision 48). The admin check
// and the capacity notice are one Store transaction; Store.Usage runs outside
// it (a PRAGMA on SQLite). The budget is the one of the store's engine; only
// when the measure is over it does a second Read ask what eviction could still
// delete (EvictionLeft), which decides the tier. usageDoc builds the document.
func (s Service) Usage(ctx context.Context, identity string) (bus.Usage, error) {
	var notice string
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		notice = ""
		if err := s.RequireServerAdmin(ctx, tx, identity); err != nil {
			return err
		}
		n, ok, err := tx.Notice(ctx, noticeCapacity)
		if err != nil {
			return err
		}
		if ok {
			notice = n.Message
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
	budget, _, err := core.Budget(s.Cfg, s.Store.Engine())
	if err != nil {
		return bus.Usage{}, err
	}
	var left store.EvictionLeft
	if u.Bytes > budget {
		err = s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
			var e error
			left, e = tx.EvictionLeft(ctx, core.EvictionCutoffs(s.Cfg, s.NowMs()))
			return e
		})
		if err != nil {
			return bus.Usage{}, err
		}
	}
	out := usageDoc(u, budget, core.TargetBytes(budget, s.Cfg.CleanupFreePercent), left)
	out.Notice = notice
	return out, nil
}

// usageDoc is the usage document without the notice: the enforced measure
// against the budget, the disk size with its ratio (0 when there are no
// bytes, never a division by zero) and the pressure tier.
func usageDoc(u store.Usage, budget, target int64, left store.EvictionLeft) bus.Usage {
	out := bus.Usage{Bytes: u.Bytes, BudgetBytes: budget, Evictable: true, DiskBytes: u.DiskBytes}
	if u.Bytes > 0 {
		out.DiskRatio = float64(u.DiskBytes) / float64(u.Bytes)
	}
	out.Tier = rules.StorageTier(u.Bytes, budget, target, left.Early(), left.Late())
	return out
}
