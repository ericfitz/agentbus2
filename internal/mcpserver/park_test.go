package mcpserver_test

import (
	"testing"

	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func parkedOf(t *testing.T, e *testEnv, sid string) bool {
	t.Helper()
	f, err := sessionstate.Read(e.DataDir, sid)
	if err != nil {
		t.Fatal(err)
	}
	return f.Parked
}

func TestToolCallsUnparkButReceiveDoesNot(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	park := func() {
		t.Helper()
		if err := sessionstate.SetParked(e.DataDir, sid, true); err != nil {
			t.Fatal(err)
		}
	}
	park()
	r.must(t, "receive", map[string]any{"session": sid})
	if !parkedOf(t, e, sid) {
		t.Fatal("receive unparked the session")
	}
	// A failing call still counts: the agent is working again.
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"send", map[string]any{"channel": "general", "content": "hi", "session": sid}},
		{"list_channels", nil},
		{"discover", nil},
		{"history", map[string]any{"channel": "general"}},
	} {
		park()
		if out, be := r.call(t, tc.name, tc.args); be != nil && out == nil && tc.name != "history" {
			t.Fatalf("%s: %v", tc.name, be)
		}
		if parkedOf(t, e, sid) {
			t.Errorf("%s left the session parked", tc.name)
		}
	}
	// Unparked: a second call is a no-op that succeeds.
	r.must(t, "list_channels", nil)
	if parkedOf(t, e, sid) {
		t.Fatal("parked after a no-op unpark")
	}
}

func TestToolCallUnparksAfterServerRestartWithoutRegister(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	first := startServer(t, e, tbl, "claude-code")
	sid := str(first.must(t, "register", nil), "session")
	if err := sessionstate.SetParked(e.DataDir, sid, true); err != nil {
		t.Fatal(err)
	}
	tbl.kill(42)
	second := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 43 })
	second.must(t, "list_channels", nil)
	if parkedOf(t, e, sid) {
		t.Fatal("a tool call on a restarted server left the session parked")
	}
}

func TestHintedRegisterUnparksThePrimary(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "claude-code")
	sid := str(r.must(t, "register", nil), "session")
	if err := sessionstate.SetParked(e.DataDir, sid, true); err != nil {
		t.Fatal(err)
	}
	r.must(t, "register", map[string]any{"hint": "other"})
	if parkedOf(t, e, sid) {
		t.Fatal("a hinted register left the primary parked")
	}
}

func TestRegisterClearsAParkLeftByADeadSession(t *testing.T) {
	e := newTestEnv(t)
	tbl := newFakeTable()
	first := startServer(t, e, tbl, "claude-code")
	sid := str(first.must(t, "register", nil), "session")
	if err := sessionstate.SetParked(e.DataDir, sid, true); err != nil {
		t.Fatal(err)
	}
	tbl.kill(42)
	second := startServer(t, e, tbl, "claude-code", func(o *mcpserver.Options) { o.Self = 43 })
	got := second.must(t, "register", nil)
	if got["resumed"] != true || str(got, "session") != sid {
		t.Fatalf("register: %v", got)
	}
	if parkedOf(t, e, sid) {
		t.Fatal("register left the resumed session parked")
	}
}
