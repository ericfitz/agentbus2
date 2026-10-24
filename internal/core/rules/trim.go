package rules

import (
	"encoding/json"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// TrimFramingBytes is a generous, fixed allowance for one record's own array
// punctuation (comma, whitespace) in the enclosing JSON array.
// TrimHardCeilingBytes is the absolute cap on any trimmed result, regardless
// of what limit a caller (or misconfiguration) asks for.
//
// Callers with sibling result fields (batch, gaps, notice, next, ...) compute
// their exact reserve, the real serialized size of a placeholder result with
// the item list cleared (MarshalLen), and subtract it from the limit before
// calling TrimToBytes.
const (
	TrimFramingBytes     = 32
	TrimHardCeilingBytes = 4 << 20
)

// TrimToBytes keeps whole items up to limit bytes as measured by size plus the
// per-record framing allowance, clamped to the 4 MiB hard ceiling. The first
// item is always kept even if it alone exceeds the limit: a legal
// max-message-size record above the result default must still be deliverable,
// or a receiver could never make progress past it.
func TrimToBytes[T any](items []T, size func(T) int, limit int) []T {
	limit = min(limit, TrimHardCeilingBytes)
	total := 0
	for i, it := range items {
		total += size(it) + TrimFramingBytes
		if total > limit && i > 0 {
			return items[:i]
		}
	}
	return items
}

// MarshalLen returns the serialized size of v. Every caller passes a value
// built entirely of strings, ints, bools and slices of those, so json.Marshal
// cannot fail.
func MarshalLen(v any) int {
	j, _ := json.Marshal(v)
	return len(j)
}

// EnvelopeBytes measures the serialized size of a Message.
func EnvelopeBytes(m bus.Message) int { return MarshalLen(m) }

const (
	// upperBoundSeqDigits is one more than int64's 19-digit maximum, for margin.
	upperBoundSeqDigits = 20
	// upperBoundCreatedAtDigits is a Unix millisecond timestamp's width until 2286.
	upperBoundCreatedAtDigits = 13
)

// EnvelopeUpperBound measures the worst-case wire size of a message before it
// is written. m carries the real values known at call time and has Seq and
// CreatedAt unset: those are sized to their maximum digit width. MemoryID and
// Revision are omitempty pointers only ever set on a memory-channel write
// (memoryKind), so they are sized the same way, but only then.
func EnvelopeUpperBound(m bus.Message, memoryKind bool) int {
	size := EnvelopeBytes(m)
	// Seq and CreatedAt are always-present fields; the zero value already
	// contributes one digit ("0") each, so add the rest of their worst-case width.
	size += (upperBoundSeqDigits - 1) + (upperBoundCreatedAtDigits - 1)
	// sender_kind is always present and sized as the longer kind, so a message
	// from a human fits whatever the caller put in m.
	if n := len(bus.IdentityHuman) - len(m.SenderKind); n > 0 {
		size += n
	}
	if memoryKind {
		size += len(`,"memory_id":`) + upperBoundSeqDigits
		size += len(`,"revision":`) + upperBoundSeqDigits
	}
	return size
}
