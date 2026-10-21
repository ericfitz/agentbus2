package sqlite

import (
	"strings"
	"testing"
	"time"
)

// Ported from v1 internal/bus/dsn_test.go (TestSQLiteURIPath).
// A Windows absolute path has no leading slash, so as a URI path it must
// gain one: file:///C:/... rather than file://C:%5C..., which SQLite
// parses as an authority and rejects ("invalid uri authority").
func TestURIPath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/Users/pat/agentbus2.db", "/Users/pat/agentbus2.db"},
		{"C:/Users/pat/agentbus2.db", "/C:/Users/pat/agentbus2.db"},
	} {
		if got := uriPath(tc.in); got != tc.want {
			t.Errorf("uriPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Ported from v1 internal/bus/dsn_test.go (TestSQLiteDSNHasEmptyAuthority).
func TestDSNHasEmptyAuthority(t *testing.T) {
	dsn, err := DSN(t.TempDir()+"/agentbus2.db", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dsn, "file:///") || strings.Contains(dsn, `%5C`) {
		t.Fatalf("dsn = %q, want file:/// and no escaped backslashes", dsn)
	}
}

func TestDSNCarriesPragmas(t *testing.T) {
	dsn, err := DSN(t.TempDir()+"/agentbus2.db", 250*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_txlock=immediate", "busy_timeout(250)", "journal_mode(WAL)", "foreign_keys(1)"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("dsn %q lacks %q", dsn, want)
		}
	}
	def, err := DSN(t.TempDir()+"/agentbus2.db", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "busy_timeout(5000)") {
		t.Errorf("default dsn %q lacks busy_timeout(5000)", def)
	}
}
