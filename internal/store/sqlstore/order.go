package sqlstore

import (
	"slices"
	"strings"
)

// sortByBytes orders xs by the byte order of key(x). SQLite's BINARY
// collation sorts text that way, but Postgres sorts under the database
// collation (en_US.utf8 puts "alpha_1" before "alpha-2" and "Émile"
// before "kim"), so a list the contract orders "by name" or "by key" is
// sorted here instead of by an ORDER BY on a text column (Decision 54: the
// engines agree). Elements with equal keys end up in no particular order.
func sortByBytes[T any](xs []T, key func(T) string) {
	slices.SortFunc(xs, func(a, b T) int { return strings.Compare(key(a), key(b)) })
}
