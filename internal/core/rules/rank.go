package rules

import "strings"

// RankDigits is the rank alphabet in ASCII (byte) order, so comparing keys as
// strings compares them as base-62 fractions.
const RankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// RankBetween returns a key strictly between a and b in byte order; ""
// means unbounded on that side. Keys are base-62 fractions ("V" is about one
// half), and a key can always grow by a digit, so a midpoint always exists
// and sibling ranks never need rebalancing. Keys never end in '0': nothing
// sorts between "x" and "x0". Precondition: a < b when both are set. A byte
// outside RankDigits (only reachable from a corrupt row) is clamped to 0
// rather than indexing IndexByte's -1.
func RankBetween(a, b string) string {
	var out strings.Builder
	for i := 0; ; i++ {
		lo := 0
		if i < len(a) {
			lo = strings.IndexByte(RankDigits, a[i])
		}
		if lo < 0 {
			lo = 0
		}
		hi := len(RankDigits)
		if i < len(b) {
			hi = strings.IndexByte(RankDigits, b[i])
		}
		if hi < 0 {
			hi = 0
		}
		if hi-lo > 1 {
			out.WriteByte(RankDigits[(lo+hi)/2])
			return out.String()
		}
		// Adjacent or equal digits: keep a's digit and go one place deeper.
		// If the digits differed, the result is already below b, so b stops
		// bounding the remaining places.
		out.WriteByte(RankDigits[lo])
		if hi != lo {
			b = ""
		}
	}
}

// lastDigitIndex returns the alphabet index of s's last byte, floored at 1
// (never 0) so callers that decrement from it can never produce the reserved
// trailing '0'. A byte outside RankDigits floors the same way.
func lastDigitIndex(s string) int {
	idx := strings.IndexByte(RankDigits, s[len(s)-1])
	if idx < 1 {
		idx = 1
	}
	return idx
}

// RankAfter returns the smallest key strictly greater than a (a must be
// non-empty). Repeated appends call this rather than RankBetween(a, ""),
// which would grow the key a byte every ~6 appends: it increments a's last
// digit in place, and once that is the alphabet's top ('z') extends with a
// fresh digit, so growth is one byte per 61 appends.
func RankAfter(a string) string {
	idx := strings.IndexByte(RankDigits, a[len(a)-1])
	if idx < 0 {
		idx = 0
	}
	if idx < len(RankDigits)-1 {
		return a[:len(a)-1] + string(RankDigits[idx+1])
	}
	return a + string(RankDigits[1])
}

// RankBefore returns the largest key strictly less than b (b must be
// non-empty), symmetric to RankAfter for repeated prepends. It decrements b's
// last digit in place while that stays above the reserved '1' floor; past
// that it replaces the last digit with '0' followed by 'z' ('0' is fine
// anywhere but last).
func RankBefore(b string) string {
	idx := lastDigitIndex(b)
	if idx > 1 {
		return b[:len(b)-1] + string(RankDigits[idx-1])
	}
	return b[:len(b)-1] + string(RankDigits[0]) + string(RankDigits[len(RankDigits)-1])
}
