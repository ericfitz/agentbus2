package token

import (
	"testing"
	"time"
)

// TestExternalDefaultsLiveInTheTokenPackage: an External with nothing set
// reads email, groups and sub and mints a 900 s human bearer; callers pass
// the configured values or nothing, never defaults of their own.
func TestExternalDefaultsLiveInTheTokenPackage(t *testing.T) {
	var e External
	if got := e.EffectiveHumanLifetime(); got != 900*time.Second {
		t.Errorf("default lifetime %v", got)
	}
	if got := e.Claims.orDefault(); got != (ClaimNames{Email: "email", Groups: "groups", Subject: "sub"}) {
		t.Errorf("default claims %+v", got)
	}
	e.HumanLifetime = time.Hour
	if got := e.EffectiveHumanLifetime(); got != time.Hour {
		t.Errorf("explicit lifetime %v", got)
	}
}
