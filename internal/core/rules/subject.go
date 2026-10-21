package rules

import (
	"strings"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// MaxSubjectRunes bounds a message subject (v1 ADR 0010).
const MaxSubjectRunes = 200

// NormalizeSubject trims s; an empty result means no subject. A line break or
// more than MaxSubjectRunes characters is a validation error naming the field.
func NormalizeSubject(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", bus.Errorf(bus.CodeValidation, false, "subject must be a single line")
	}
	if utf8.RuneCountInString(s) > MaxSubjectRunes {
		return "", bus.Errorf(bus.CodeValidation, false, "subject must be at most %d characters", MaxSubjectRunes)
	}
	return s, nil
}
