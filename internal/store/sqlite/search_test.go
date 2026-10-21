package sqlite

import (
	"errors"
	"testing"

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

func TestTextSearchSQLite(t *testing.T) {
	cond, args, err := dialect{}.TextSearch(search.Parse(`rate limit e159* "commit hash"`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `m.seq IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`; cond != want {
		t.Errorf("cond = %q, want %q", cond, want)
	}
	want := `"rate" "limit" "e159"` + ftsPrefix + ` "commit hash"`
	if len(args) != 1 || args[0] != want {
		t.Errorf("args = %#v, want [%q]", args, want)
	}

	_, args, err = dialect{}.TextSearch(search.Parse(`say"hi"`))
	if err != nil {
		t.Fatal(err)
	}
	// The parser reads say"hi" as one word containing quotes; each is doubled.
	if want := `"say""hi"""`; len(args) != 1 || args[0] != want {
		t.Errorf("args = %#v, want [%q]", args, want)
	}
}

func TestTextSearchSQLiteEmpty(t *testing.T) {
	for _, q := range []string{"", "***", `""`, "!!!"} {
		_, _, err := dialect{}.TextSearch(search.Parse(q))
		if !errors.Is(err, store.ErrInvalid) {
			t.Errorf("query %q: err = %v, want ErrInvalid", q, err)
		}
	}
}
