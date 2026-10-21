package sqlstore

import "testing"

func TestSqliteConstraint(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
		msg  string
		ext  []int
		text string
		want bool
	}{
		{"extended code in the list", 2067, "", []int{1555, 2067}, "UNIQUE constraint failed", true},
		{"primary 19 with the text", 19, "constraint failed: UNIQUE constraint failed: identities.name (19)", []int{1555, 2067}, "UNIQUE constraint failed", true},
		{"primary 19 for another constraint", 19, "FOREIGN KEY constraint failed (19)", []int{1555, 2067}, "UNIQUE constraint failed", false},
		{"extended code of another constraint", 787, "FOREIGN KEY constraint failed (787)", []int{1555, 2067}, "UNIQUE constraint failed", false},
		{"not a constraint error", 5, "database is locked (5)", []int{787}, "FOREIGN KEY constraint failed", false},
		{"text without the constraint code", 1, "UNIQUE constraint failed", []int{1555, 2067}, "UNIQUE constraint failed", false},
	} {
		if got := sqliteConstraint(c.code, c.msg, c.ext, c.text); got != c.want {
			t.Errorf("%s: sqliteConstraint(%d, %q) = %v, want %v", c.name, c.code, c.msg, got, c.want)
		}
	}
}
