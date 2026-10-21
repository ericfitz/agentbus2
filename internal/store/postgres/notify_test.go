package postgres

import (
	"strings"
	"testing"
)

func TestPayloadRoundTrip(t *testing.T) {
	if got := EncodePayload(12, 34); got != "12:34" {
		t.Fatalf("EncodePayload = %q", got)
	}
	for _, c := range []struct {
		in   string
		a, b int64
	}{{"12:34", 12, 34}, {"0:0", 0, 0}} {
		a, b, ok := ParsePayload(c.in)
		if !ok || a != c.a || b != c.b {
			t.Fatalf("ParsePayload(%q) = %d, %d, %v", c.in, a, b, ok)
		}
	}
}

func TestParsePayloadRejects(t *testing.T) {
	for _, in := range []string{"", "12", "a:b", "1:2:3", "-1:2", " 1:2", "1:2\n", "99999999999999999999:1", "1:99999999999999999999", ":1", "1:", "+1:2"} {
		if _, _, ok := ParsePayload(in); ok {
			t.Errorf("ParsePayload(%q) accepted", in)
		}
	}
}

// The channel name must stay within Postgres's 63-byte identifier limit
// however long the schema name is: "agentbus2_" (10) + 24 hex = 34 bytes.
func TestChannelQueryIsBounded(t *testing.T) {
	if !strings.Contains(ChannelQuery, "left(") || !strings.Contains(ChannelQuery, "24)") {
		t.Fatalf("ChannelQuery does not bound the name: %s", ChannelQuery)
	}
}
