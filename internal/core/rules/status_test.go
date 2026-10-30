package rules

import (
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestCheckStatusTag(t *testing.T) {
	for _, tag := range bus.SettableStatusTags() {
		if err := CheckStatusTag(tag); err != nil {
			t.Errorf("%q: %v", tag, err)
		}
	}
	if n := len(bus.SettableStatusTags()); n != 7 {
		t.Fatalf("settable tags = %d, want 7", n)
	}
	const list = "idle, planning, implementing, reviewing, waiting, blocked, needs-human"
	cases := []struct {
		tag  bus.StatusTag
		want string
	}{
		{"", "tag is required: one of " + list},
		{bus.StatusOffline, "tag offline is set by the bus for an agent with no live session; set one of " + list},
		{"Idle", `unknown tag "Idle": one of ` + list},
		{"done", `unknown tag "done": one of ` + list},
	}
	for _, tc := range cases {
		err := CheckStatusTag(tc.tag)
		wantCode(t, err, bus.CodeValidation)
		if m := errMsg(err); m != tc.want {
			t.Errorf("%q: got %q, want %q", tc.tag, m, tc.want)
		}
	}
}

func TestNormalizeLine(t *testing.T) {
	if got, err := NormalizeLine("text", " a "); err != nil || got != "a" {
		t.Fatalf("trim: %q %v", got, err)
	}
	if _, err := NormalizeLine("text", strings.Repeat("é", 200)); err != nil {
		t.Fatalf("200 runes: %v", err)
	}
	_, err := NormalizeLine("text", strings.Repeat("é", 201))
	wantCode(t, err, bus.CodeValidation)
	if m := errMsg(err); m != "text must be at most 200 characters" {
		t.Errorf("201 runes: %q", m)
	}
	for _, bad := range []string{"a\nb", "a\rb"} {
		_, err := NormalizeLine("text", bad)
		wantCode(t, err, bus.CodeValidation)
		if m := errMsg(err); m != "text must be a single line" {
			t.Errorf("%q: %q", bad, m)
		}
	}
	_, err = NormalizeSubject("a\nb")
	if m := errMsg(err); m != "subject must be a single line" {
		t.Errorf("subject text changed: %q", m)
	}
}

// errMsg is the message of a bus error, "" for nil.
func errMsg(err error) string {
	var be *bus.Error
	if errors.As(err, &be) {
		return be.Message
	}
	return ""
}
