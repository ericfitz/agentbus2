package rules

import (
	"strings"
	"unicode/utf8"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// MaxLineRunes bounds a one-line text field: a message subject, a status text
// or a status problem.
const MaxLineRunes = 200

// NormalizeLine trims s; a line break or more than MaxLineRunes characters is
// a validation error naming field. An empty result is returned as is.
func NormalizeLine(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, "\r\n") {
		return "", bus.Errorf(bus.CodeValidation, false, "%s must be a single line", field)
	}
	if utf8.RuneCountInString(s) > MaxLineRunes {
		return "", bus.Errorf(bus.CodeValidation, false, "%s must be at most %d characters", field, MaxLineRunes)
	}
	return s, nil
}

const settableTagList = "idle, planning, implementing, reviewing, waiting, blocked, needs-human"

// CheckStatusTag accepts exactly the settable tags: a missing tag, offline
// (set by the bus only) and any other value are validation errors.
func CheckStatusTag(t bus.StatusTag) error {
	for _, ok := range bus.SettableStatusTags() {
		if t == ok {
			return nil
		}
	}
	switch t {
	case "":
		return bus.Errorf(bus.CodeValidation, false, "tag is required: one of %s", settableTagList)
	case bus.StatusOffline:
		return bus.Errorf(bus.CodeValidation, false, "tag offline is set by the bus for an agent with no live session; set one of %s", settableTagList)
	default:
		return bus.Errorf(bus.CodeValidation, false, "unknown tag %q: one of %s", string(t), settableTagList)
	}
}
