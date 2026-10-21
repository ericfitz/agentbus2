package rules

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func TestNormalizeSubject(t *testing.T) {
	got, err := NormalizeSubject("  hello  ")
	if err != nil || got != "hello" {
		t.Fatalf("trim: %q %v", got, err)
	}
	if got, err := NormalizeSubject("   "); err != nil || got != "" {
		t.Fatalf("blank: %q %v", got, err)
	}
	for _, bad := range []string{"a\nb", "a\rb"} {
		_, err := NormalizeSubject(bad)
		wantCode(t, err, bus.CodeValidation)
	}
	if _, err := NormalizeSubject(strings.Repeat("é", MaxSubjectRunes)); err != nil {
		t.Fatalf("200 runes must pass: %v", err)
	}
	_, err = NormalizeSubject(strings.Repeat("é", MaxSubjectRunes+1))
	wantCode(t, err, bus.CodeValidation)
}
