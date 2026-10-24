package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/store"
)

// humanConn creates the human identity email directly in the Store (there is
// no sign-in path in a local profile) and opens it with a live session.
func humanConn(t *testing.T, e *testEnv, email string) *client.Conn {
	t.Helper()
	ctx := context.Background()
	boot := e.conn(t, "boot") // opening a Conn creates its identity as an agent, so create the human first
	now := boot.Core.NowMs()
	if err := boot.Core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.CreateHuman(ctx, store.HumanBinding{Identity: email, ClaimType: store.ClaimEmail, Value: email}, now)
	}); err != nil {
		t.Fatal(err)
	}
	c := e.conn(t, email)
	if _, err := c.Bus.Open(ctx, bus.OpenSessionInput{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestToolDescriptionsNameHumanSender(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	res, err := r.CS.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, tl := range res.Tools {
		if tl.Name != "receive" && tl.Name != "history" {
			continue
		}
		seen++
		for _, want := range []string{"sender_kind", "human", "your operator", "steering"} {
			if !strings.Contains(tl.Description, want) {
				t.Errorf("%s description lacks %q: %s", tl.Name, want, tl.Description)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("saw %d of receive and history", seen)
	}
}

func TestDiscoverShowsKind(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	r.must(t, "register", nil)
	humanConn(t, e, "eric@corp.example")
	d := r.must(t, "discover", nil)
	kinds := map[string]string{}
	for _, x := range arr(d, "identities") {
		m := x.(map[string]any)
		kinds[str(m, "name")] = str(m, "kind")
	}
	if kinds["eric@corp.example"] != "human" || kinds["pat"] != "agent" {
		t.Fatalf("discover kinds = %v", kinds)
	}
}

func TestReceiveCarriesSenderKind(t *testing.T) {
	e := newTestEnv(t)
	r := startServer(t, e, newFakeTable(), "test-client")
	sid := str(r.must(t, "register", nil), "session")
	r.must(t, "subscribe", map[string]any{"channel": "general"})
	eric := humanConn(t, e, "eric@corp.example")
	if _, err := eric.Bus.Send(context.Background(), bus.SendInput{Channel: "general", Content: "steer"}); err != nil {
		t.Fatal(err)
	}
	got := r.must(t, "receive", map[string]any{"session": sid})
	msgs := arr(got, "messages")
	if len(msgs) != 1 || str(msgs[0].(map[string]any), "sender_kind") != "human" {
		t.Fatalf("receive: %v", got)
	}
}
