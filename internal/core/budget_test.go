package core

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestBudgetPicksEngineSetting(t *testing.T) {
	c := Defaults()
	c.SQLiteBudgetMiB, c.PostgresBudgetMiB = 100, 200
	for _, tc := range []struct {
		e       store.Engine
		bytes   int64
		setting string
	}{
		{store.EngineSQLite, 100 << 20, "sqlite_budget_mib"},
		{store.EnginePostgres, 200 << 20, "postgres_budget_mib"},
	} {
		b, s, err := Budget(c, tc.e)
		if err != nil || b != tc.bytes || s != tc.setting {
			t.Errorf("Budget(%s) = %d, %q, %v", tc.e, b, s, err)
		}
	}
	_, _, err := Budget(c, store.Engine("mysql"))
	if be, ok := err.(*bus.Error); !ok || be.Code != bus.CodeInternal {
		t.Errorf("unknown engine: %v", err)
	}
}

func TestTargetBytes(t *testing.T) {
	if got := TargetBytes(1000, 25); got != 750 {
		t.Errorf("TargetBytes = %d, want 750", got)
	}
}

func TestEvictionCutoffs(t *testing.T) {
	const h = 3_600_000
	now := int64(1_000 * h)
	got := EvictionCutoffs(Defaults(), now)
	want := store.EvictionCutoffs{
		TombstoneBefore:    now - 72*h,
		TokenExpiredBefore: now,
		IdleBefore:         now - 168*h,
		MessageBefore:      now - 1*h,
	}
	if got != want {
		t.Errorf("EvictionCutoffs = %+v, want %+v", got, want)
	}
}
