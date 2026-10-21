package store

import (
	"strings"
	"testing"
)

func TestNameRule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" = valid, else substring of the error
	}{
		{"simple", "ok", ""},
		{"punctuation", "a-b.c", ""},
		{"max length", strings.Repeat("a", 128), ""},
		{"empty", "", "must be 1-128 bytes"},
		{"too long", strings.Repeat("a", 129), "must be 1-128 bytes"},
		{"slash", "a/b", "must not contain '/'"},
		{"control", "a\x01b", "must not contain control characters"},
		{"tab", "tab\there", "must not contain control characters"},
		{"invalid UTF-8", "a\xffb", "must be valid UTF-8"},
		{"truncated rune", "\xc3", "must be valid UTF-8"},
		{"NUL", "a\x00b", "must not contain"},
		{"non-ASCII", "\u00c9mile", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := NameRule(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("NameRule(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NameRule(%q) = %v, want error containing %q", tc.in, err, tc.want)
			}
		})
	}
}

func TestTextRule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" = valid, else substring of the error
	}{
		{"empty", "", ""},
		{"ASCII", "kid-1", ""},
		{"non-ASCII", "\u00c9mile \u2603", ""},
		{"control other than NUL", "a\x01b\tc", ""},
		{"invalid byte", "a\xffb", "must be valid UTF-8"},
		{"truncated rune", "\xc3", "must be valid UTF-8"},
		// utf8.ValidString accepts NUL, but Postgres refuses it in TEXT (22021).
		{"NUL", "a\x00b", "must not contain NUL"},
		{"trailing NUL", "{}\x00", "must not contain NUL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := TextRule(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("TextRule(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("TextRule(%q) = %v, want error containing %q", tc.in, err, tc.want)
			}
		})
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []Kind{KindOrdinary, KindMemory, KindTasks} {
		if !ValidKind(k) {
			t.Errorf("ValidKind(%q) = false, want true", k)
		}
	}
	for _, k := range []Kind{"", "dm", "Ordinary"} {
		if ValidKind(k) {
			t.Errorf("ValidKind(%q) = true, want false", k)
		}
	}
}

func TestFileNameRule(t *testing.T) {
	for _, ok := range []string{"pat", "a-b.c", "con-man", "console", "com10", "lpt", "nul2x", "a.con"} {
		if err := FileNameRule(ok); err != nil {
			t.Errorf("FileNameRule(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a/b", "a\x00b", ".", "..", `a\b`, "a:b", ":", "CON", "con", "Con.json", "prn", "AUX", "nul", "NUL.txt",
		"com0", "COM1", "com9", "lpt1", "LPT9", "com\u00b9", "LPT\u00b3.log"} {
		if err := FileNameRule(bad); err == nil {
			t.Errorf("FileNameRule(%q) accepted", bad)
		}
	}
}
