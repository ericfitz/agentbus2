package bus

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/dbtest"
	ab2store "github.com/ericfitz/agentbus2/internal/store"
)

const adminRequired = "administrator privilege required"

// forceScope writes a scope onto an identity row by raw SQL, the way an
// earlier build or a manual edit could: the Store refuses to.
func (h *harness) forceScope(name, scope string) {
	h.t.Helper()
	db, err := sql.Open(h.e.Driver, h.dsn)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	q := "UPDATE identities SET scope = ? WHERE name = ?"
	if h.e.Name == "postgres" {
		q = "UPDATE identities SET scope = $1 WHERE name = $2"
	}
	if _, err := db.Exec(q, scope, name); err != nil {
		h.t.Fatalf("force scope on %s: %v", name, err)
	}
}

// TestScopeGateServer: a scoped agent is refused every server-administration
// operation with exactly the answer an unscoped non-administrator gets, so
// nothing depends on what exists (spec 4.1 (a), (d)); a stored row holding the
// administrator flag (or kind human) and a scope reads back unscoped (spec
// 4.3, 4.5); the Identities listing is the See rule (Task 3.1).
func TestScopeGateServer(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		coder := h.scopedIdentity("job1-coder", "job1")
		h.scopedIdentity("job1-peer", "job1")
		pat := h.localAs("pat")
		h.localAdmin("root")
		h.human("eric@corp.example")

		calls := map[string]func(b ab2bus.Bus) error{
			"Status": func(b ab2bus.Bus) error { _, err := b.Status(ctx); return err },
			"Usage":  func(b ab2bus.Bus) error { _, err := b.Usage(ctx); return err },
			"ListScopes": func(b ab2bus.Bus) error {
				_, err := b.ListScopes(ctx)
				return err
			},
			"CreateAgentIdentity": func(b ab2bus.Bus) error {
				_, err := b.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "fresh"})
				return err
			},
			"ListBridges": func(b ab2bus.Bus) error { _, err := b.ListBridges(ctx); return err },
			"Reset":       func(b ab2bus.Bus) error { return b.Reset(ctx, ab2bus.ResetInput{Scope: ab2bus.ResetData}) },
			"GetBridge":   func(b ab2bus.Bus) error { _, err := b.GetBridge(ctx, "other"); return err },
		}
		for op, call := range calls {
			scoped, plain := call(coder), call(pat)
			codeOf(t, scoped, ab2bus.CodeForbidden)
			codeOf(t, plain, ab2bus.CodeForbidden)
			if msgOf(scoped) != msgOf(plain) {
				t.Errorf("%s: scoped %q, unscoped %q; want byte-equal", op, msgOf(scoped), msgOf(plain))
			}
			if op != "ListBridges" && op != "GetBridge" && msgOf(scoped) != adminRequired {
				t.Errorf("%s: %q, want %q", op, msgOf(scoped), adminRequired)
			}
		}

		// Identities: the See rule.
		for _, n := range []string{"job1-coder", "job1-peer", "pat", "root", "eric@corp.example"} {
			h.session(n, n)
		}
		names := func(b ab2bus.Bus) []string {
			t.Helper()
			ids, err := b.Identities(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var out []string
			for _, i := range ids {
				out = append(out, i.Name)
			}
			slices.Sort(out)
			return out
		}
		if got, want := names(coder), []string{"eric@corp.example", "job1-coder", "job1-peer", "root"}; !slices.Equal(got, want) {
			t.Errorf("scoped Identities = %v, want %v", got, want)
		}
		if got, want := names(pat), []string{"eric@corp.example", "job1-coder", "job1-peer", "pat", "root"}; !slices.Equal(got, want) {
			t.Errorf("unscoped Identities = %v, want %v", got, want)
		}

		// A stored administrator row holding a scope is an ordinary
		// administrator on every read path.
		h.admin("dual")
		h.session("dual", "dual")
		h.forceScope("dual", "job1")
		h.forceScope("eric@corp.example", "job1")
		dual := h.localAs("dual")
		if _, err := dual.Status(ctx); err != nil {
			t.Errorf("administrator row with a scope: Status = %v, want ok", err)
		}
		h.tx(func(ctx context.Context, tx ab2store.Tx) error {
			for _, n := range []string{"dual", "eric@corp.example"} {
				id, err := tx.Identity(ctx, n)
				if err != nil || id.Scope != "" {
					t.Errorf("Identity(%s) = %+v, %v; want no scope", n, id, err)
				}
			}
			all, err := tx.ListIdentities(ctx)
			if err != nil {
				return err
			}
			for _, id := range all {
				if (id.Admin || id.Kind == ab2store.IdentityHuman) && id.Scope != "" {
					t.Errorf("ListIdentities: %s still scoped %q", id.Name, id.Scope)
				}
			}
			return nil
		})
		ids, err := dual.Identities(ctx)
		if err != nil || len(ids) != 6 {
			t.Errorf("administrator row with a scope: Identities = %d, %v; want everyone (6)", len(ids), err)
		}
	})
}

// TestCreateAgentIdentityScopedRefusals: through the Bus, a scoped
// CreateAgentIdentity for an existing human or administrator row is
// validation with the 4.3/4.5 texts; unscoped for a human stays conflict.
func TestCreateAgentIdentityScopedRefusals(t *testing.T) {
	dbtest.ForEachEngine(t, func(t *testing.T, e dbtest.Engine) {
		h := newHarness(t, e, harnessOptions{})
		ctx := context.Background()
		h.rtScope("job1")
		root := h.localAdmin("root")
		h.human("eric@corp.example")
		for name, want := range map[string]string{"root": "an administrator cannot be scoped", "eric@corp.example": "a human identity cannot be scoped"} {
			_, err := root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: name, Scope: "job1"})
			codeOf(t, err, ab2bus.CodeValidation)
			if msgOf(err) != want {
				t.Errorf("%s: %q, want %q", name, msgOf(err), want)
			}
		}
		_, err := root.CreateAgentIdentity(ctx, ab2bus.CreateAgentIdentityInput{Name: "eric@corp.example"})
		codeOf(t, err, ab2bus.CodeConflict)
	})
}
