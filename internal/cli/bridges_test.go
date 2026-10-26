package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// fakeBridgeBus answers CreateBridge from a canned result and records the
// input; the rest of the Bus is the opened one.
type fakeBridgeBus struct {
	bus.Bus
	got bus.CreateBridgeInput
	res bus.BridgeCreated
}

func (f *fakeBridgeBus) CreateBridge(_ context.Context, in bus.CreateBridgeInput) (bus.BridgeCreated, error) {
	f.got = in
	return f.res, nil
}

func TestBridgesCreateOutputMapping(t *testing.T) {
	e := adminEnv(t)
	fake := &fakeBridgeBus{}
	testConn = func(c *client.Conn) { fake.Bus = c.Bus; c.Bus = fake }
	t.Cleanup(func() { testConn = nil })
	run := runner(e, RunBridges)

	// A live bridge with two members not invited: every field is mapped.
	fake.res = bus.BridgeCreated{
		Bridge: bus.Bridge{Channel: "job1-chat", Scope: "job1", SlackChannelID: "C123", SlackName: "jc",
			State: bus.BridgeLive, CreatedAt: 7, ArchivedAt: 9},
		NotInvited: []bus.BridgeNotInvited{{Identity: "a@corp.example", Reason: "users_not_found"}, {Identity: "b", Reason: "no_email"}},
	}
	code, out, er := run("create", "-slack-name", "jc", "job1-chat")
	if code != 0 {
		t.Fatalf("create: %d %s", code, er)
	}
	if fake.got.Channel != "job1-chat" || fake.got.SlackName != "jc" {
		t.Fatalf("input = %+v", fake.got)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("out %q: %v", out, err)
	}
	want := map[string]any{
		"channel": "job1-chat", "scope": "job1", "slack_channel_id": "C123", "slack_name": "jc",
		"state": "live", "created_at": float64(7), "archived_at": float64(9),
		"not_invited": []any{
			map[string]any{"identity": "a@corp.example", "reason": "users_not_found"},
			map[string]any{"identity": "b", "reason": "no_email"},
		},
	}
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("out = %s\nwant %s", gb, wb)
	}

	// A creating bridge with nobody to report: optional members are absent and
	// not_invited is an empty array, never null.
	fake.res = bus.BridgeCreated{Bridge: bus.Bridge{Channel: "c", State: bus.BridgeCreating, CreatedAt: 1}}
	code, out, er = run("create", "c")
	if code != 0 {
		t.Fatalf("create: %d %s", code, er)
	}
	if fake.got.SlackName != "" {
		t.Fatalf("slack name = %q, want none", fake.got.SlackName)
	}
	got = nil
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if ni, ok := got["not_invited"].([]any); !ok || len(ni) != 0 {
		t.Fatalf("not_invited = %#v in %s", got["not_invited"], out)
	}
	for _, k := range []string{"scope", "slack_channel_id", "slack_name", "archived_at"} {
		if _, ok := got[k]; ok {
			t.Errorf("member %q present in %s", k, out)
		}
	}
	if got["state"] != "creating" {
		t.Errorf("state = %v", got["state"])
	}
}

func TestBridgesCommandsUnconfigured(t *testing.T) {
	e := adminEnv(t)
	run := runner(e, RunBridges)
	for _, args := range [][]string{
		{"list"}, {"get", "general"}, {"create", "general"}, {"create", "-slack-name", "x", "general"}, {"remove", "general"},
	} {
		code, _, er := run(args...)
		if code != 1 || !strings.Contains(er, "agentbus2: unavailable: the Slack bridge is not configured") {
			t.Fatalf("%v: %d %q", args, code, er)
		}
	}
	for _, args := range [][]string{{"create"}, {"get"}, {"remove"}, {"frob"}, {}, {"list", "extra"}} {
		if code, _, _ := run(args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}
