package store

import (
	"errors"
	"strings"
	"unicode"
)

// NameRule is the name rule for identities, channels and ACL identities:
// 1-128 UTF-8 bytes, no control characters, no '/' (the '/' ban is what
// Decision 93 relies on). The error is a plain sentence without a subject so
// callers can supply their own. Ported from v1 internal/bus/sessions.go.
func NameRule(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return errors.New("must be 1-128 bytes")
	}
	if strings.ContainsRune(name, '/') {
		return errors.New("must not contain '/'")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// ValidKind reports whether k is a stored channel kind.
func ValidKind(k Kind) bool {
	return k == KindOrdinary || k == KindMemory || k == KindTasks
}
