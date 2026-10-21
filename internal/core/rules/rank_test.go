package rules

import (
	"math/rand"
	"sort"
	"testing"
)

func TestRankBetweenOrdersStrictly(t *testing.T) {
	cases := [][2]string{{"", ""}, {"", "V"}, {"V", ""}, {"A", "B"}, {"A", "A1"}, {"Az", "B"}, {"", "1"}, {"zz", ""}, {"A", "A01"}}
	for _, c := range cases {
		m := RankBetween(c[0], c[1])
		if m == "" || (c[0] != "" && m <= c[0]) || (c[1] != "" && m >= c[1]) {
			t.Fatalf("RankBetween(%q,%q)=%q is not strictly between", c[0], c[1], m)
		}
		if m[len(m)-1] == '0' {
			t.Fatalf("RankBetween(%q,%q)=%q ends in '0'", c[0], c[1], m)
		}
	}
}

// A byte outside RankDigits (a corrupt row) is clamped, never an index panic.
func TestRankBetweenClampsOffAlphabetBytes(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RankBetween panicked: %v", r)
		}
	}()
	if m := RankBetween("-", "0"); m == "" {
		t.Fatal("RankBetween(\"-\",\"0\") returned empty")
	}
	if m := RankBetween("~", ""); m == "" {
		t.Fatal("RankBetween(\"~\",\"\") returned empty")
	}
}

// Repeated appends or prepends at one end grow the key by about a byte per
// 61 operations and keep every invariant.
func TestRankAfterBeforeStayShort(t *testing.T) {
	key := RankBetween("", "")
	for i := 0; i < 5000; i++ {
		next := RankAfter(key)
		if next <= key {
			t.Fatalf("append %d: %q not after %q", i, next, key)
		}
		if next[len(next)-1] == '0' {
			t.Fatalf("append %d: %q ends in '0'", i, next)
		}
		key = next
	}
	if len(key) > 90 {
		t.Fatalf("5000 appends grew to %d bytes: %q", len(key), key)
	}

	key = RankBetween("", "")
	for i := 0; i < 5000; i++ {
		prev := RankBefore(key)
		if prev == "" {
			t.Fatalf("prepend %d produced the empty string", i)
		}
		if prev >= key {
			t.Fatalf("prepend %d: %q not before %q", i, prev, key)
		}
		if prev[len(prev)-1] == '0' {
			t.Fatalf("prepend %d: %q ends in '0'", i, prev)
		}
		key = prev
	}
	if len(key) > 90 {
		t.Fatalf("5000 prepends grew to %d bytes: %q", len(key), key)
	}
}

func TestRankSurvivesManyInserts(t *testing.T) {
	keys := []string{RankBetween("", "")}
	for i := 0; i < 500; i++ {
		keys = append([]string{RankBetween("", keys[0])}, keys...)
	}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		j := r.Intn(len(keys) + 1)
		lo, hi := "", ""
		if j > 0 {
			lo = keys[j-1]
		}
		if j < len(keys) {
			hi = keys[j]
		}
		k := RankBetween(lo, hi)
		keys = append(keys[:j], append([]string{k}, keys[j:]...)...)
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatal("keys are not sorted")
	}
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			t.Fatalf("duplicate key %q", keys[i])
		}
	}
}
