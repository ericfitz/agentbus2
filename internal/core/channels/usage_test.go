package channels

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestUsageDoc(t *testing.T) {
	const budget, target = 100, 80
	for _, tc := range []struct {
		name  string
		u     store.Usage
		left  store.EvictionLeft
		tier  bus.PressureTier
		ratio float64
	}{
		{"no bytes, disk held: ratio 0", store.Usage{Bytes: 0, DiskBytes: 50}, store.EvictionLeft{}, bus.PressureNormal, 0},
		{"at the target", store.Usage{Bytes: 80, DiskBytes: 160}, store.EvictionLeft{Useless: true}, bus.PressureNormal, 2},
		{"between target and budget", store.Usage{Bytes: 100, DiskBytes: 100}, store.EvictionLeft{}, bus.PressureApproaching, 1},
		{"over, useless left", store.Usage{Bytes: 101, DiskBytes: 101}, store.EvictionLeft{Useless: true}, bus.PressureSoft, 1},
		{"over, expired left", store.Usage{Bytes: 101, DiskBytes: 101}, store.EvictionLeft{Expired: true, Ordinary: true}, bus.PressureSoft, 1},
		{"over, only idle left", store.Usage{Bytes: 101, DiskBytes: 101}, store.EvictionLeft{Idle: true}, bus.PressureHard, 1},
		{"over, only ordinary left", store.Usage{Bytes: 101, DiskBytes: 101}, store.EvictionLeft{Ordinary: true}, bus.PressureHard, 1},
		{"over, nothing left", store.Usage{Bytes: 101, DiskBytes: 101}, store.EvictionLeft{}, bus.PressureExhausted, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := usageDoc(tc.u, budget, target, tc.left)
			if got.Tier != tc.tier || got.DiskRatio != tc.ratio || !got.Evictable ||
				got.Bytes != tc.u.Bytes || got.BudgetBytes != budget || got.DiskBytes != tc.u.DiskBytes {
				t.Fatalf("usageDoc = %+v, want tier %q ratio %v", got, tc.tier, tc.ratio)
			}
		})
	}
}
