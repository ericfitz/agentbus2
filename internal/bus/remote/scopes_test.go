package remote

import (
	"context"
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

// These are wire documents with one closed-enum member outside its set, as a server speaking another protocol would send.
const (
	badScopeState  = `{"name":"j","state":"paused","created_at":1,"last_activity":1}`
	badScopeRetire = `{"name":"j","state":"done","retire":"later","created_at":1,"last_activity":1}`
	badBridgeState = `{"channel":"c","scope":"j","slack_name":"c","state":"paused","created_at":1}`
)

// A bad enum from the server reaches the caller as an internal error naming
// the member, through every operation that returns the type (the conversion
// is tested alone above; this drives the whole HTTP path).
func TestRemoteRefusesUnknownEnumFromServer(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		body   string
		member string
		call   func(b *Bus) error
	}{
		{"ListScopes-state", `{"scopes":[` + badScopeState + `]}`, "state", func(b *Bus) error { _, err := b.ListScopes(ctx); return err }},
		{"ListScopes-retire", `{"scopes":[` + badScopeRetire + `]}`, "retire", func(b *Bus) error { _, err := b.ListScopes(ctx); return err }},
		{"GetScope-state", badScopeState, "state", func(b *Bus) error { _, err := b.GetScope(ctx, "j"); return err }},
		{"CreateScope-state", badScopeState, "state", func(b *Bus) error { _, err := b.CreateScope(ctx, bus.CreateScopeInput{Name: "j"}); return err }},
		{"ListBridges-state", `{"bridges":[` + badBridgeState + `]}`, "state", func(b *Bus) error { _, err := b.ListBridges(ctx); return err }},
		{"GetBridge-state", badBridgeState, "state", func(b *Bus) error { _, err := b.GetBridge(ctx, "c"); return err }},
		{"CreateBridge-state", `{"bridge":` + badBridgeState + `,"not_invited":[]}`, "state", func(b *Bus) error {
			_, err := b.CreateBridge(ctx, bus.CreateBridgeInput{Channel: "c"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{status: 200, ctype: "application/json", body: tc.body}
			wantInternal(t, tc.call(New(newRecClient(t, rec))), tc.member)
		})
	}
}

// DoneScope refuses a retire mode outside the set before sending anything;
// the two members are sent.
func TestRemoteDoneScopeRefusesUnknownRetire(t *testing.T) {
	ctx := context.Background()
	rec := &recorder{status: 200, ctype: "application/json", body: `{"name":"j","state":"done","retire":"normal","created_at":1,"last_activity":1}`}
	b := New(newRecClient(t, rec))
	for _, m := range []bus.RetireMode{"later", "", "NORMAL"} {
		_, err := b.DoneScope(ctx, bus.DoneScopeInput{Name: "j", Retire: m})
		if bus.CodeOf(err) != bus.CodeValidation || !strings.Contains(err.Error(), "retire") {
			t.Fatalf("retire %q: err = %v, want validation naming retire", m, err)
		}
	}
	if rec.method != "" {
		t.Fatalf("a request was sent: %s %s", rec.method, rec.path)
	}
	for _, m := range []bus.RetireMode{bus.RetireWithBridge, bus.RetireNormal} {
		if _, err := b.DoneScope(ctx, bus.DoneScopeInput{Name: "j", Retire: m}); err != nil {
			t.Fatalf("retire %q: %v", m, err)
		}
		if !strings.Contains(string(rec.reqBody), `"retire":"`+string(m)+`"`) {
			t.Fatalf("retire %q: body %s", m, rec.reqBody)
		}
	}
}
