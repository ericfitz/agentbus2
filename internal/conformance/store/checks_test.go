package store

import (
	"fmt"
	"testing"

	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
	"github.com/ericfitz/agentbus2/internal/store/sqlstore"
)

// TestCheckConstraintsRefuseUnknownValues inserts raw rows, bypassing the
// Store, to prove the database itself refuses an unknown value of each
// enumerated column. Every column gets a control insert with a valid value
// first, so a refusal can only come from its CHECK.
func TestCheckConstraintsRefuseUnknownValues(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		s, _, rawDSN := openStore(t, e, sqlstore.Options{})
		mustIdentity(t, s, "pat")
		ch := mustChannel(t, s, "chk", ab2store.KindOrdinary, "pat", nil)
		db := rawDB(t, e, rawDSN)
		seq := 900000
		for _, c := range []struct {
			name string
			good string
			// ins builds the insert for a value of the column under test, n
			// making the row's key unique.
			ins func(v string, n int) string
			bad string
		}{
			{"identities.kind", "human", func(v string, n int) string {
				return fmt.Sprintf("INSERT INTO identities (name, is_admin, tags_acked_seq, created_at, kind) VALUES ('raw%d', FALSE, 0, 1, '%s')", n, v)
			}, "robot"},
			{"channels.kind", "memory", func(v string, n int) string {
				return fmt.Sprintf("INSERT INTO channels (name, kind, creator, created_seq, created_at) VALUES ('raw%d', '%s', '', 1, 1)", n, v)
			}, "stream"},
			{"messages.sender_kind", "human", func(v string, n int) string {
				seq++
				return fmt.Sprintf("INSERT INTO messages (seq, channel_id, sender, created_at, content, sender_kind) VALUES (%d, %d, 'pat', 1, 'x', '%s')", seq, ch.ID, v)
			}, "robot"},
			{"human_bindings.claim_type", "email", func(v string, n int) string {
				return fmt.Sprintf("INSERT INTO human_bindings (identity, claim_type, value, created_at) VALUES ('pat', '%s', 'v%d', 1)", v, n)
			}, "phone"},
			{"access_refs.ref_list", "users", func(v string, n int) string {
				return fmt.Sprintf("INSERT INTO access_refs (ref_list, ref_kind, value, created_at) VALUES ('%s', 'email', 'v%d', 1)", v, n)
			}, "owners"},
			{"access_refs.ref_kind", "group", func(v string, n int) string {
				return fmt.Sprintf("INSERT INTO access_refs (ref_list, ref_kind, value, created_at) VALUES ('users', '%s', 'v%d', 1)", v, n)
			}, "phone"},
		} {
			n := len(c.name) * 100
			if _, err := db.Exec(c.ins(c.good, n)); err != nil {
				t.Fatalf("%s: control insert with %q: %v", c.name, c.good, err)
			}
			if _, err := db.Exec(c.ins(c.bad, n+1)); err == nil {
				t.Errorf("%s: insert with unknown value %q succeeded, want a CHECK violation", c.name, c.bad)
			}
		}
	})
}
