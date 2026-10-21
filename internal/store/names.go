package store

import (
	"errors"
	"fmt"
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

// FileNameRule is NameRule for a name a client also uses as a file or
// directory name (an identity's credential and token files, a profile's
// directories): besides NameRule it refuses "." and "..", the separators and
// drive mark of other platforms ('\\' and ':'), and the names Windows
// reserves for devices (CON, PRN, AUX, NUL, COM0-9, LPT0-9 and their
// superscript-digit forms), in any case and with any extension.
func FileNameRule(name string) error {
	if err := NameRule(name); err != nil {
		return err
	}
	if name == "." || name == ".." {
		return errors.New("must not be a dot name")
	}
	if strings.ContainsAny(name, `\:`) {
		return errors.New(`must not contain '\' or ':'`)
	}
	base, _, _ := strings.Cut(name, ".")
	switch b := strings.ToUpper(base); {
	case b == "CON", b == "PRN", b == "AUX", b == "NUL":
		return fmt.Errorf("must not be the Windows device name %s", b)
	case len(b) > 3 && (b[:3] == "COM" || b[:3] == "LPT"):
		switch d := b[3:]; d {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
			return fmt.Errorf("must not be the Windows device name %s", b)
		}
	}
	return nil
}

// ValidKind reports whether k is a stored channel kind.
func ValidKind(k Kind) bool {
	return k == KindOrdinary || k == KindMemory || k == KindTasks
}
