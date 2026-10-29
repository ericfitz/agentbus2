package slackbridge

import (
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
)

func TestSendErrorClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"duplicate", core.E(bus.CodeConflict, "x"), classDuplicate},
		{"retryable conflict", bus.Errorf(bus.CodeConflict, true, "x"), classRetry},
		{"validation", core.E(bus.CodeValidation, "x"), classInvalid},
		{"forbidden", core.E(bus.CodeForbidden, "x"), classForbidden},
		{"rejected", core.E(bus.CodeInspectionRejected, "x"), classRejected},
		{"unavailable", bus.Errorf(bus.CodeUnavailable, true, "x"), classRetry},
		{"rate limited", bus.Errorf(bus.CodeRateLimited, true, "x"), classRetry},
		{"internal", core.E(bus.CodeInternal, "x"), classRetry},
		{"plain", errors.New("boom"), classRetry},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sendErrorClass(c.err); got != c.want {
				t.Errorf("sendErrorClass = %q, want %q", got, c.want)
			}
		})
	}
}
