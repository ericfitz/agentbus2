package core

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// E builds a bus.Error whose retryable flag is the code's fixed one:
// rate_limited, unavailable and internal are retryable, the others not.
func E(code bus.Code, format string, args ...any) *bus.Error {
	retryable := code == bus.CodeRateLimited || code == bus.CodeUnavailable || code == bus.CodeInternal
	return bus.Errorf(code, retryable, format, args...)
}

// FromStore maps a Store error to a bus.Error: nil stays nil, a *bus.Error
// passes through, and the Store sentinels map to not_found, conflict,
// validation (with the store's reason text), unavailable (Decision 69) and
// internal.
func FromStore(err error) error {
	if err == nil {
		return nil
	}
	var be *bus.Error
	if errors.As(err, &be) {
		return err
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return E(bus.CodeNotFound, "%s", reason(err, store.ErrNotFound))
	case errors.Is(err, store.ErrConflict):
		return E(bus.CodeConflict, "%s", reason(err, store.ErrConflict))
	case errors.Is(err, store.ErrInvalid):
		return E(bus.CodeValidation, "%s", reason(err, store.ErrInvalid))
	case errors.Is(err, store.ErrRetryable):
		return E(bus.CodeUnavailable, "%s", reason(err, store.ErrRetryable))
	}
	return bus.Internal(err)
}

// reason is err's text without the sentinel's own prefix, so a wrapped
// "store: invalid input: name must not contain '/'" reads "name must not
// contain '/'".
func reason(err, sentinel error) string {
	s := err.Error()
	if rest, ok := strings.CutPrefix(s, sentinel.Error()+": "); ok {
		return rest
	}
	if s == sentinel.Error() {
		return strings.TrimPrefix(s, "store: ")
	}
	return s
}

// RowRejected is an Embedder refusing an input itself (v1 embed.go
// rowRejected: 400, 413, 422), so retrying that text alone fails the same
// way. Auth, rate-limit, server and network errors are not a RowRejected.
type RowRejected struct {
	// Index is the rejected text's position in the Embed call, -1 when the
	// endpoint does not say.
	Index  int
	Reason string
}

func (e *RowRejected) Error() string {
	if e.Index < 0 {
		return fmt.Sprintf("embedder rejected the input: %s", e.Reason)
	}
	return fmt.Sprintf("embedder rejected input %d: %s", e.Index, e.Reason)
}

// IsRowRejected reports whether err is or wraps a *RowRejected.
func IsRowRejected(err error) bool {
	var r *RowRejected
	return errors.As(err, &r)
}
