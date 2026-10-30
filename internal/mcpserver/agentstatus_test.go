package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// open note: agent-status 9
func TestSetStatusDefaultsToOwnSession(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	be := r.failure(t, "set_status", map[string]any{"tag": "planning", "text": "x"}, bus.CodeValidation)
	if want := "set_status needs a session: call register first, or pass session"; be.Message != want {
		t.Fatalf("message = %q, want %q", be.Message, want)
	}
	reg := r.must(t, "register", nil)
	out := r.must(t, "set_status", map[string]any{"tag": "planning", "text": "x"})
	if str(out, "tag") != "planning" || str(out, "text") != "x" || str(out, "display_name") != str(reg, "display_name") {
		t.Fatalf("set_status = %v, register = %v", out, reg)
	}
	if _, ok := out["problem"]; ok {
		t.Errorf("problem present when unset: %v", out)
	}
	if out["revision"] == nil || out["set_at"] == nil {
		t.Errorf("revision or set_at missing: %v", out)
	}
	out = r.must(t, "set_status", map[string]any{"tag": "blocked", "text": "y", "problem": "z", "session": str(reg, "session")})
	if str(out, "problem") != "z" {
		t.Fatalf("problem = %v", out)
	}
}

func TestSetStatusUnknownTagFromServer(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	r.must(t, "register", nil)
	be := r.failure(t, "set_status", map[string]any{"tag": "bogus", "text": "x"}, bus.CodeValidation)
	if !strings.Contains(be.Message, `unknown tag "bogus"`) {
		t.Fatalf("message = %q", be.Message)
	}
}

func TestDiscoverCarriesStatus(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	r.must(t, "register", nil)
	// sam is live but has set nothing; pat sets a status.
	sam := e.conn(t, "sam")
	if _, err := sam.Bus.Open(t.Context(), bus.OpenSessionInput{}); err != nil {
		t.Fatal(err)
	}
	r.must(t, "set_status", map[string]any{"tag": "implementing", "text": "the board", "problem": "none"})

	find := func(list []any, name string) map[string]any {
		t.Helper()
		for _, o := range list {
			if m := o.(map[string]any); str(m, "name") == name {
				return m
			}
		}
		t.Fatalf("%s not listed in %v", name, list)
		return nil
	}
	check := func(label string, ids []any) {
		t.Helper()
		pat := find(ids, "pat")
		st, ok := pat["status"].(map[string]any)
		if !ok || str(st, "tag") != "implementing" || str(st, "text") != "the board" || str(st, "problem") != "none" || str(st, "display_name") == "" || st["set_at"] == nil || st["revision"] == nil {
			t.Fatalf("%s: pat status = %v", label, pat["status"])
		}
		if _, has := find(ids, "sam")["status"]; has {
			t.Errorf("%s: sam has a status key without setting one", label)
		}
	}
	check("discover", arr(r.must(t, "discover", nil), "identities"))
	check("register", arr(r.must(t, "register", map[string]any{"hint": "second"}), "others"))
}

func TestSetStatusUnparks(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	if err := sessionstate.SetParked(e.DataDir, sid, true); err != nil {
		t.Fatal(err)
	}
	r.must(t, "set_status", map[string]any{"tag": "idle", "text": "parked"})
	if parkedOf(t, e, sid) {
		t.Fatal("set_status left the session parked")
	}
}
