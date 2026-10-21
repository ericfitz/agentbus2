package cli

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/procs"
)

// The exit-code errors of watch and wait (spec 3.5, Decision 42, v1 ADR 0021).
var (
	errReplaced    = errors.New("replaced")
	errHarnessGone = procs.ErrHarnessGone
	errTimeout     = errors.New("timeout")
)

// exitCode maps the outcome of watch or wait to the spec 3.5 table: nil is 0
// (woke), a timeout 1, replaced 3, harness gone 4, anything else 2.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errTimeout):
		return 1
	case errors.Is(err, errReplaced), bus.CodeOf(err) == bus.CodeReplaced:
		return 3
	case errors.Is(err, errHarnessGone):
		return 4
	}
	return 2
}

// cancelCause is v1 waitCanceled: a context that is not done leaves err alone;
// a canceled one is read through context.Cause before err is looked at,
// because a canceled remote Watch returns a wrapped transport error, not
// context.Canceled. The harness watch's cause is errHarnessGone; anything
// else (the signal context included) means replaced.
func cancelCause(ctx context.Context, err error) error {
	if ctx.Err() == nil {
		return err
	}
	if errors.Is(context.Cause(ctx), errHarnessGone) {
		return errHarnessGone
	}
	return errReplaced
}
