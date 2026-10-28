package rules

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestStorageTier(t *testing.T) {
	for _, tc := range []struct {
		name        string
		bytes       int64
		early, late bool
		want        bus.PressureTier
	}{
		{"at target", 750, true, true, bus.PressureNormal},
		{"above target", 751, true, true, bus.PressureApproaching},
		{"at budget", 1000, false, false, bus.PressureApproaching},
		{"over, early and late", 1001, true, true, bus.PressureSoft},
		{"over, early only", 1001, true, false, bus.PressureSoft},
		{"over, late only", 1001, false, true, bus.PressureHard},
		{"over, none", 1001, false, false, bus.PressureExhausted},
		{"empty", 0, false, false, bus.PressureNormal},
	} {
		if got := StorageTier(tc.bytes, 1000, 750, tc.early, tc.late); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, p := range []bus.PressureTier{bus.PressureNormal, bus.PressureApproaching, bus.PressureSoft, bus.PressureHard, bus.PressureExhausted} {
		if !bus.ValidPressureTier(p) {
			t.Errorf("%q invalid", p)
		}
	}
	if bus.ValidPressureTier("bogus") || bus.ValidPressureTier("") {
		t.Error("bogus tier valid")
	}
}
