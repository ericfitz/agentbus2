package rules

import "github.com/ericfitz/agentbus2/internal/bus"

// StorageTier is the pressure tier of a store holding bytes against a budget
// and a sweep target. Above the budget the tier is a fact of what is still
// eligible, not of the sweep's history: soft while step 1 or 2 has an eligible
// row (early), hard when only step 3 or 4 has one (late), exhausted when none
// has. At or below the target it is normal; above the target and at or below
// the budget, approaching.
//
// open note: postgres-budget 1
func StorageTier(bytes, budget, target int64, early, late bool) bus.PressureTier {
	switch {
	case bytes <= target:
		return bus.PressureNormal
	case bytes <= budget:
		return bus.PressureApproaching
	case early:
		return bus.PressureSoft
	case late:
		return bus.PressureHard
	}
	return bus.PressureExhausted
}
