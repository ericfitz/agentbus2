package maintenance

import (
	"context"
	"fmt"
	"math"

	"github.com/ericfitz/agentbus2/internal/store"
)

// capacityKind is the notice kind Status and Receive report.
const capacityKind = "capacity"

// budget is the SQLite budget in bytes: BudgetOverride (tests), else the
// setting.
func (s Service) budget() int64 {
	if s.BudgetOverride > 0 {
		return s.BudgetOverride
	}
	return int64(s.Cfg.SQLiteBudgetMiB) << 20
}

// evictToBudget deletes ordinary messages oldest-first while usage exceeds the
// budget, down to the target (budget less CleanupFreePercent of it), then
// reclaims freed pages. Only where Store.Usage is not Logical (Milestone 7
// Gate 0 item 2, Decision (Eric, 2026-10-21)); elsewhere it does nothing.
//
// Ported from v1 internal/bus/maintenance.go evictToBudget. The sweep starts
// only once usage exceeds the budget itself (v1's hysteresis), not the lower
// target it sweeps to. The sweep ends at the target, when a chunk deletes
// nothing, or when the tick deadline passes; the trailing Reclaim is
// unconditional. Idempotent: a second run at or below the budget deletes
// nothing.
func (s Service) evictToBudget(ctx context.Context, _ int64) (int, error) {
	u, err := s.Store.Usage(ctx)
	if err != nil {
		return 0, err
	}
	if u.Logical { // interim: Postgres is not enforced until the Postgres budget task
		return 0, nil
	}
	budget := s.budget()
	target := budget - budget*int64(s.Cfg.CleanupFreePercent)/100
	total := 0
	if u.Bytes > budget {
		for u.Bytes > target && !s.pastDeadline(ctx) {
			n, err := s.deleteOrdinaryChunk(ctx, math.MaxInt64)
			total += n
			if err != nil {
				return total, err
			}
			if n == 0 {
				break
			}
			if err := s.Store.Reclaim(ctx); err != nil {
				return total, err
			}
			if u, err = s.Store.Usage(ctx); err != nil {
				return total, err
			}
		}
	}
	return total, s.Store.Reclaim(ctx)
}

// capacityNotice sets the capacity notice while usage exceeds the budget and
// clears it otherwise (v1 evictToBudget's notice half, Milestone 7 Gate 0 item
// 3). Where usage is not evictable nothing of ours is set, so it neither sets
// nor clears. It returns 1 when it set the notice, else 0. Idempotent.
func (s Service) capacityNotice(ctx context.Context, now int64) (int, error) {
	u, err := s.Store.Usage(ctx)
	if err != nil {
		return 0, err
	}
	if u.Logical { // interim, as in evictToBudget
		return 0, nil
	}
	budget := s.budget()
	over := u.Bytes > budget
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		if over {
			return tx.SetNotice(ctx, store.Notice{Kind: capacityKind, Message: capacityText(budget), SetAt: now})
		}
		return tx.ClearNotice(ctx, capacityKind)
	})
	if err != nil || !over {
		return 0, err
	}
	return 1, nil
}

// capacityText is the notice message; the number is the setting's unit, MiB.
func capacityText(budgetBytes int64) string {
	return fmt.Sprintf("agentbus2 capacity exhausted: sqlite_budget_mib=%d is filled by live memories and protected tombstones, which are never evicted automatically; raise sqlite_budget_mib and restart the server, or have an administrator reset the bus to discard all data", budgetBytes>>20)
}
