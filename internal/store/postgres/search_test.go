package postgres

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestTextSearchPostgres(t *testing.T) {
	cond, args, err := dialect{}.TextSearch(search.Parse(`rate limit e159* "commit hash"`))
	if err != nil {
		t.Fatal(err)
	}
	term := func(x string) string { return "(m.fts_subject @@ " + x + " OR m.fts_content @@ " + x + ")" }
	want := strings.Join([]string{term(tsWord), term(tsWord), term(tsPrefix), term(tsWord)}, " AND ")
	if cond != want {
		t.Errorf("cond = %q, want %q", cond, want)
	}
	wantArgs := []any{"rate", "rate", "limit", "limit", "e159", "e159", "commit hash", "commit hash"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("args = %#v, want %#v", args, wantArgs)
	}
	if n := strings.Count(cond, "?"); n != len(args) {
		t.Errorf("%d placeholders, %d args", n, len(args))
	}
}

func TestTextSearchPostgresEmpty(t *testing.T) {
	for _, q := range []string{"", "***", `""`, "!!!"} {
		_, _, err := dialect{}.TextSearch(search.Parse(q))
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("query %q: err = %v, want ErrInvalid", q, err)
		}
	}
}

func TestTextSearchPostgresConstants(t *testing.T) {
	for _, lit := range []string{`'''$'`, `''':*'`} {
		if !strings.Contains(tsPrefix, lit) {
			t.Errorf("tsPrefix lacks %s: %s", lit, tsPrefix)
		}
	}
}
