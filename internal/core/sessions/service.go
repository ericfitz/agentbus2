// Package sessions holds the session rules: open and resume, close, display
// names, offered cursors and the identities list (Decisions 15, 39, 40, 44,
// 75, 79). Ported from v1 internal/bus/sessions.go, re-shaped to the v2
// decisions; harness identity, owner rows and the subscription wipe on a
// fresh start are not ported (Decisions 14, 78).
package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Service is the session aggregate over a Core.
type Service struct{ *core.Core }

// resumeTokenBytes is the size of a resume token before encoding (plan
// choice 4).
const resumeTokenBytes = 32

// DisplayName is the first free of identity, identity-2, identity-3, ...;
// with a hint, identity/hint, identity/hint-2, ... (Decision 39, plan choice
// 5). taken holds the display names of the identity's rows with ClosedAt == 0
// (live and lapsed-resumable), never of closed rows. Pure.
func DisplayName(identity, hint string, taken []string) string {
	base := identity
	if hint != "" {
		base = identity + "/" + hint
	}
	used := make(map[string]struct{}, len(taken))
	for _, n := range taken {
		used[n] = struct{}{}
	}
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		name := base + "-" + strconv.Itoa(i)
		if _, ok := used[name]; !ok {
			return name
		}
	}
}

// hashToken is the SHA-256 hex stored in sessions.resume_token_hash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validToken reports whether token has the shape Open generates: 32 bytes,
// base64url without padding.
func validToken(token string) bool {
	b, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(b) == resumeTokenBytes
}

// Require returns the identity's session for a session-scoped call: not_found
// unless the row belongs to identity, session_closed for a closed row,
// session_lapsed past the lease plus the skew (pending Eric: Milestone 3 item
// 2; Open with the resume token revives it), and otherwise renews the lease.
func (s Service) Require(ctx context.Context, tx store.Tx, identity, sessionID string) (store.Session, error) {
	row, err := tx.Session(ctx, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, core.E(bus.CodeNotFound, "session %q does not exist", sessionID)
	}
	if err != nil {
		return store.Session{}, core.FromStore(err)
	}
	if row.Identity != identity {
		return store.Session{}, core.E(bus.CodeNotFound, "session %q does not exist", sessionID)
	}
	if row.ClosedAt != 0 {
		return store.Session{}, core.E(bus.CodeSessionClosed, "session %q is closed", sessionID)
	}
	now := s.NowMs()
	if !s.IsLive(row, now) {
		return store.Session{}, core.E(bus.CodeSessionLapsed, "session %q lapsed; open with its resume token to resume it", sessionID)
	}
	until := s.LeaseUntil(now)
	if err := tx.RenewLease(ctx, sessionID, until); err != nil {
		return store.Session{}, core.FromStore(err)
	}
	row.LeaseExpiresAt = until
	return row, nil
}
