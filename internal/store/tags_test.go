package store

import "testing"

func TestTagRange(t *testing.T) {
	tests := []struct{ in, lo, hi string }{
		{"env", "env", "env"},
		{"env*", "env", "env~"},
		{"env:*", "env:", "env:~"},
	}
	for _, tc := range tests {
		lo, hi := TagRange(tc.in)
		if lo != tc.lo || hi != tc.hi {
			t.Errorf("TagRange(%q) = %q, %q; want %q, %q", tc.in, lo, hi, tc.lo, tc.hi)
		}
		p := NewTagPattern(tc.in)
		if p != (TagPattern{tc.in, tc.lo, tc.hi}) {
			t.Errorf("NewTagPattern(%q) = %+v", tc.in, p)
		}
	}
}
