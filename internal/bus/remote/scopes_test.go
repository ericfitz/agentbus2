package remote

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

func wantInternal(t *testing.T, err error, member string) {
	t.Helper()
	be, ok := err.(*bus.Error)
	if !ok || be.Code != bus.CodeInternal || !strings.Contains(be.Message, member) {
		t.Fatalf("err = %v, want an internal *bus.Error naming %s", err, member)
	}
}

func TestFromGenScopeRejectsUnknownState(t *testing.T) {
	_, err := fromGenScope(gen.Scope{Name: "j", State: "paused"})
	wantInternal(t, err, "state")
	retire := gen.ScopeRetire("later")
	_, err = fromGenScope(gen.Scope{Name: "j", State: gen.ScopeState("done"), Retire: &retire})
	wantInternal(t, err, "retire")

	for _, st := range []string{"open", "done", "retired"} {
		sc, err := fromGenScope(gen.Scope{Name: "j", State: gen.ScopeState(st)})
		if err != nil || string(sc.State) != st {
			t.Fatalf("state %q = %+v, %v", st, sc, err)
		}
	}
	for _, m := range []string{"with_bridge", "normal"} {
		r := gen.ScopeRetire(m)
		sc, err := fromGenScope(gen.Scope{Name: "j", State: "done", Retire: &r})
		if err != nil || string(sc.Retire) != m {
			t.Fatalf("retire %q = %+v, %v", m, sc, err)
		}
	}
}

func TestFromGenBridgeRejectsUnknownState(t *testing.T) {
	_, err := fromGenBridge(gen.Bridge{Channel: "c", State: "paused"})
	wantInternal(t, err, "state")
	for _, st := range []string{"creating", "live", "archived"} {
		b, err := fromGenBridge(gen.Bridge{Channel: "c", State: gen.BridgeState(st)})
		if err != nil || string(b.State) != st {
			t.Fatalf("state %q = %+v, %v", st, b, err)
		}
	}
}
