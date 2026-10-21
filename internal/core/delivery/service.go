// Package delivery holds the receive and watch rules (Decisions 16, 40, 42,
// 45, 68, 75, 95): batch tokens, session-scoped cursors with the
// per-identity acked mark, and the wake primitive. Ported from v1
// internal/bus/receive.go (receiveOnce, makeBatchToken), internal/bus/wait.go
// (peek) and internal/cli/watch.go (the merge gap), re-keyed from the
// registered name to the session. Not ported: Receive's wait loop (Decision
// 83), the polling guard, expiry, the inbox and the tags/ row.
package delivery

import (
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/core/sessions"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Service is the delivery aggregate over a Core.
type Service struct {
	*core.Core
	Sessions sessions.Service
}

// redeliveryHardCeilingBytes bounds a redelivered batch's own size,
// independent of result_default_kib (which can change between calls and must
// not shrink "the same batch").
const redeliveryHardCeilingBytes = rules.TrimHardCeilingBytes

// maxBatchTokenLen is the fixed length of every batch token: RandomHex(12)
// plus the ":o0"/":o1" include_own suffix. It sizes the placeholder used to
// reserve space for a new-selection token that is not yet minted.
const maxBatchTokenLen = 24 + len(":o1")

// maxNoticesPerReceive bounds how many Gaps one receive reports, so the
// metadata alone cannot approach the hard ceiling. Gap cursor advances apply
// only to the reported subset; the rest surface on a later call.
const maxNoticesPerReceive = 256

// makeBatchToken carries the include_own flag the batch was created with
// (":o1"/":o0"), so a redelivery replays the original selection predicate.
func makeBatchToken(includeOwn bool) (string, error) {
	tok, err := core.RandomHex(12)
	if err != nil {
		return "", bus.Internal(err)
	}
	if includeOwn {
		return tok + ":o1", nil
	}
	return tok + ":o0", nil
}

func tokenIncludesOwn(token string) bool { return strings.HasSuffix(token, ":o1") }

// ownFilter excludes the session's own messages unless includeOwn (pending
// Eric: Milestone 3 item 1).
func ownFilter(sessionID string, includeOwn bool) store.MessageFilter {
	if includeOwn {
		return store.MessageFilter{}
	}
	return store.MessageFilter{ExcludeSessionID: sessionID}
}

// tagSets converts stored tag subscriptions for rules.MatchedTags.
func tagSets(subs []store.TagSubscription) []rules.TagSet {
	out := make([]rules.TagSet, len(subs))
	for i, sub := range subs {
		ps := make([]string, len(sub.Patterns))
		for j, p := range sub.Patterns {
			ps[j] = p.Pattern
		}
		out[i] = rules.TagSet{Patterns: ps, CreatedSeq: sub.CreatedSeq}
	}
	return out
}
