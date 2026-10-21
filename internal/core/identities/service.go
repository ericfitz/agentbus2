// Package identities holds the rules of registration, enrollment tokens, the
// administrator flag, credential reset, the scoped data reset and the
// server's signing key (Decisions 20, 58, 59, 72, 73, 77, 92 and Eric's Gate
// 0 decisions of 2026-10-21). One rule set serves the HTTP handlers and the
// host commands.
package identities

import (
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
)

// Mode is the registration mode of a listener (Decisions 20, 72).
type Mode string

// The registration modes.
const (
	ModeOpen       Mode = "open"
	ModeEnrollment Mode = "enrollment"
	ModeClosed     Mode = "closed"
)

// ParseMode accepts the three mode names and nothing else.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeOpen, ModeEnrollment, ModeClosed:
		return m, nil
	}
	return "", core.E(bus.CodeValidation, "unknown registration mode %q: want %q, %q or %q", s, ModeOpen, ModeEnrollment, ModeClosed)
}

// Service is the identities aggregate over a Core.
type Service struct{ *core.Core }

// maxAttempts bounds the transaction runs after a store.ErrConflict: the
// first attempt plus two reruns.
const maxAttempts = 3
