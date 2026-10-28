package core

import (
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

const msPerHour = 3_600_000

// Budget returns the enforced budget in bytes of the engine and the name of
// the setting that holds it. An unknown engine is an internal error.
func Budget(c Config, e store.Engine) (bytes int64, setting string, err error) {
	switch e {
	case store.EngineSQLite:
		return int64(c.SQLiteBudgetMiB) << 20, "sqlite_budget_mib", nil
	case store.EnginePostgres:
		return int64(c.PostgresBudgetMiB) << 20, "postgres_budget_mib", nil
	}
	return 0, "", E(bus.CodeInternal, "unknown store engine %q", e)
}

// TargetBytes is the size an eviction sweep stops at: the budget less
// cleanupFreePercent of it.
func TargetBytes(budget int64, cleanupFreePercent int) int64 {
	return budget - budget*int64(cleanupFreePercent)/100
}

// EvictionCutoffs returns the timestamps (ms) the budget backstop deletes
// before: the floors hold even under pressure (Eric, 2026-10-27).
func EvictionCutoffs(c Config, now int64) store.EvictionCutoffs {
	return store.EvictionCutoffs{
		TombstoneBefore:    now - TombstoneFloorHours*msPerHour,
		TokenExpiredBefore: now,
		IdleBefore:         now - int64(c.IdleWindowHours)*msPerHour,
		MessageBefore:      now - MessageFloorHours*msPerHour,
	}
}
