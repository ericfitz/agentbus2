package sqlstore

import (
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Every caller-supplied string bound to a TEXT column passes store.TextRule
// before the query (Decision 54): SQLite would store a non-UTF-8 or NUL
// string while Postgres fails the statement raw (SQLSTATE 22021) and aborts
// the transaction. The mapping, as for identities and receipts: the caller's
// own data on a write is ErrInvalid with the field named (checkText); a key
// naming another row answers as for an unknown row, ErrNotFound on a write
// that needs the row, none or a no-op on a lookup or delete (badText).
// textArgs is the backstop under every query helper, so a string a method
// does not check still fails the same way on both engines.

// checkText runs store.TextRule over pairs of (field name, value) and maps
// the first failure to ErrInvalid naming subject and field.
func checkText(subject string, pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		if err := store.TextRule(pairs[i+1]); err != nil {
			return fmt.Errorf("%w: %s %s %s", store.ErrInvalid, subject, pairs[i], err)
		}
	}
	return nil
}

// badText reports whether any of ss fails store.TextRule: no stored row can
// hold such a key.
func badText(ss ...string) bool {
	for _, s := range ss {
		if store.TextRule(s) != nil {
			return true
		}
	}
	return false
}

// textArgs refuses (ErrInvalid) a string query argument failing
// store.TextRule.
func textArgs(args []any) error {
	for i, a := range args {
		if s, ok := a.(string); ok {
			if err := store.TextRule(s); err != nil {
				return fmt.Errorf("%w: query argument %d %s", store.ErrInvalid, i+1, err)
			}
		}
	}
	return nil
}
