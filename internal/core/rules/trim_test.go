package rules

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestTrimToBytesKeepsFirst(t *testing.T) {
	items := []string{strings.Repeat("a", 100), "b", "c"}
	size := func(s string) int { return len(s) }
	got := TrimToBytes(items, size, 10)
	if len(got) != 1 || got[0] != items[0] {
		t.Fatalf("the first item is always kept: %v", got)
	}
	if got := TrimToBytes([]string(nil), size, 10); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	// Each item costs size + framing; three of 10+32 fit in 126, not 125.
	three := []string{"0123456789", "0123456789", "0123456789"}
	if got := TrimToBytes(three, size, 3*(10+TrimFramingBytes)); len(got) != 3 {
		t.Fatalf("exact fit: %d", len(got))
	}
	if got := TrimToBytes(three, size, 3*(10+TrimFramingBytes)-1); len(got) != 2 {
		t.Fatalf("one byte short: %d", len(got))
	}
}

func TestTrimToBytesHardCeiling(t *testing.T) {
	big := make([]int, 10)
	size := func(int) int { return 1 << 20 }
	// A limit far above the ceiling is clamped to 4 MiB: 3 items of
	// 1 MiB+32 fit, the 4th crosses it.
	got := TrimToBytes(big, size, 1<<40)
	if len(got) != 3 {
		t.Fatalf("hard ceiling kept %d items", len(got))
	}
}

func TestEnvelopeUpperBoundMemoryKind(t *testing.T) {
	m := bus.Message{Channel: "c", Sender: "s", Content: "hello", Subject: "x", Tags: []string{"a"}}
	plain := EnvelopeUpperBound(m, false)
	mem := EnvelopeUpperBound(m, true)
	if mem <= plain {
		t.Fatalf("memory kind must reserve memory_id and revision: %d vs %d", mem, plain)
	}
	// It is an upper bound on any real envelope of the same message.
	real := m
	real.Seq = 9_223_372_036_854_775_807
	real.CreatedAt = 9_999_999_999_999
	id, rev := int64(9_223_372_036_854_775_807), int64(9_223_372_036_854_775_807)
	real.MemoryID, real.Revision = &id, &rev
	if EnvelopeBytes(real) > mem {
		t.Fatalf("real envelope %d exceeds the bound %d", EnvelopeBytes(real), mem)
	}
	real.MemoryID, real.Revision = nil, nil
	if EnvelopeBytes(real) > plain {
		t.Fatalf("real envelope %d exceeds the bound %d", EnvelopeBytes(real), plain)
	}
}

func TestMarshalLen(t *testing.T) {
	v := map[string]int{"a": 1}
	j, _ := json.Marshal(v)
	if MarshalLen(v) != len(j) {
		t.Fatalf("MarshalLen = %d, want %d", MarshalLen(v), len(j))
	}
}
