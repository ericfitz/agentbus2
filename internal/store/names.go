package store

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TextRule refuses a caller-supplied string bound for a TEXT column that is
// not valid UTF-8 or contains a NUL byte: SQLite stores such bytes while
// Postgres fails the statement (SQLSTATE 22021 for both; NUL is valid UTF-8,
// so utf8.ValidString alone lets it through), so both engines refuse it
// before the query instead (Decision 54). The error is a plain sentence
// without a subject, as for NameRule.
func TextRule(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("must be valid UTF-8")
	}
	if strings.IndexByte(s, 0) >= 0 {
		return errors.New("must not contain NUL")
	}
	return nil
}

// NameRule is the name rule for identities, channels and ACL identities:
// 1-128 bytes passing TextRule (valid UTF-8, no NUL), no control characters,
// no '/' (the '/' ban is what Decision 93 relies on). The error is a plain
// sentence without a subject so callers can supply their own. Ported from v1
// internal/bus/sessions.go.
func NameRule(name string) error {
	if len(name) == 0 || len(name) > 128 {
		return errors.New("must be 1-128 bytes")
	}
	if err := TextRule(name); err != nil {
		return err
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
