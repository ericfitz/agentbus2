package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfitz/agentbus2/internal/store"
)

func TestIsRetryable(t *testing.T) {
	d := dialect{}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"serialization_failure", &pgconn.PgError{Code: "40001"}, true},
		// Decision (Eric, 2026-10-21): Gate 0 item 2
		{"deadlock_detected", &pgconn.PgError{Code: "40P01"}, true},
		{"wrapped", errors.Join(errors.New("ctx"), &pgconn.PgError{Code: "40001"}), true},
		{"unique_violation", &pgconn.PgError{Code: "23505"}, false},
		{"plain", errors.New("boom"), false},
		{"nil", nil, false},
	} {
		if got := d.IsRetryable(c.err); got != c.want {
			t.Errorf("%s: IsRetryable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsReadOnlyViolation(t *testing.T) {
	d := dialect{}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"read_only_sql_transaction", &pgconn.PgError{Code: "25006"}, true},
		{"serialization_failure", &pgconn.PgError{Code: "40001"}, false},
		{"plain", errors.New("x"), false},
	} {
		if got := d.IsReadOnlyViolation(c.err); got != c.want {
			t.Errorf("%s: IsReadOnlyViolation = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWithSearchPath(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://u:p@h:5432/db":                 "postgres://u:p@h:5432/db?search_path=t_ab",
		"postgres://u:p@h:5432/db?sslmode=disable": "postgres://u:p@h:5432/db?search_path=t_ab&sslmode=disable",
	} {
		got, err := WithSearchPath(in, "t_ab")
		if err != nil {
			t.Fatalf("WithSearchPath(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("WithSearchPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"T_ab", "a-b", "", "1abc", strings.Repeat("a", 64), "a;b", "a b"} {
		if _, err := WithSearchPath("postgres://h/db", bad); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("schema %q: err = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := WithSearchPath("postgres://h/db?x=%zz", "t_ab"); err == nil {
		t.Error("unparseable DSN accepted")
	}
}

// TestBackoff: before retry n+1 the wait is uniform in [0, 10ms * 2^(n-1)),
// capped at 200 ms (choice 5; Decision (Eric, 2026-10-21): Gate 0 item 3).
func TestBackoff(t *testing.T) {
	d := dialect{}
	for attempt := 1; attempt <= 12; attempt++ {
		limit := min(10*time.Millisecond<<(attempt-1), 200*time.Millisecond)
		for range 50 {
			if got := d.Backoff(attempt); got < 0 || got >= limit {
				t.Fatalf("Backoff(%d) = %v, want in [0, %v)", attempt, got, limit)
			}
		}
	}
}
