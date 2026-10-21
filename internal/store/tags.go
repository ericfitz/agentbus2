package store

import "strings"

// TagRange maps a tag pattern to the inclusive range it matches: an exact tag
// to itself, a prefix p* to [p, p~]. Every legal tag character (-, 0-9, :,
// a-z) sorts below ~ (0x7e), so p~ is above every tag that starts with p. SQL
// compares with BETWEEN, never LIKE: LIKE is case-insensitive and cannot use
// an index under BINARY collation. Ported from v1 internal/bus/tags.go
// (tagRange).
func TagRange(pattern string) (lo, hi string) {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return p, p + "~"
	}
	return pattern, pattern
}

// TagPattern is a pattern with its precomputed range.
type TagPattern struct{ Pattern, Lo, Hi string }

// NewTagPattern fills Lo and Hi with TagRange.
func NewTagPattern(pattern string) TagPattern {
	lo, hi := TagRange(pattern)
	return TagPattern{Pattern: pattern, Lo: lo, Hi: hi}
}
