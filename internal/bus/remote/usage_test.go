package remote

import (
	"strings"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func TestUsageRejectsUnknownTier(t *testing.T) {
	_, err := fromGenUsage(gen.Usage{Bytes: 1, Tier: "melting"})
	be := asBusError(t, err)
	if be.Code != ab2bus.CodeInternal || !strings.Contains(be.Message, "tier") {
		t.Fatalf("err = %+v, want internal naming tier", be)
	}
	for _, tier := range []ab2bus.PressureTier{ab2bus.PressureNormal, ab2bus.PressureApproaching, ab2bus.PressureSoft, ab2bus.PressureHard, ab2bus.PressureExhausted} {
		u, err := fromGenUsage(gen.Usage{Bytes: 1, DiskBytes: 2, DiskRatio: 2, Tier: gen.UsageTier(tier)})
		if err != nil || u.Tier != tier || u.DiskBytes != 2 || u.DiskRatio != 2 {
			t.Errorf("tier %q: %+v, %v", tier, u, err)
		}
	}
}
