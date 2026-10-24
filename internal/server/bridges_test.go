package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
)

// stubBridges records its calls and answers from its fields.
type stubBridges struct {
	calls   []string
	created bus.BridgeCreated
	err     error
}

func (s *stubBridges) Create(_ context.Context, admin string, in bus.CreateBridgeInput) (bus.BridgeCreated, error) {
	s.calls = append(s.calls, "create:"+admin+":"+in.Channel+":"+in.SlackName)
	return s.created, s.err
}

func (s *stubBridges) List(_ context.Context, admin string) ([]bus.Bridge, error) {
	s.calls = append(s.calls, "list:"+admin)
	return nil, s.err
}

func (s *stubBridges) Get(_ context.Context, admin, channel string) (bus.Bridge, error) {
	s.calls = append(s.calls, "get:"+admin+":"+channel)
	return s.created.Bridge, s.err
}

func (s *stubBridges) Remove(_ context.Context, admin string, in bus.RemoveBridgeInput) error {
	s.calls = append(s.calls, "remove:"+admin+":"+in.Channel)
	return s.err
}

func (e *env) send(identity, method, path string) (int, string, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, "http://inproc"+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp := e.raw(identity, req)
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Content-Type")
}

func TestBridgesWire(t *testing.T) {
	stub := &stubBridges{created: bus.BridgeCreated{Bridge: bus.Bridge{
		Channel: "job1-chat", Scope: "job1", SlackName: "job1-chat", State: bus.BridgeCreating, CreatedAt: 5,
	}}}
	e := newEnv(t, envOptions{})
	e.opts.Bridges = stub
	e.identity("root", true)

	// create: the body maps to the input, not_invited is an empty array.
	st, body, _ := e.postBody("root", "/v1/admin/bridges", `{"channel":"job1-chat","slack_name":"jc"}`)
	if st != 200 || !strings.Contains(body, `"not_invited":[]`) || !strings.Contains(body, `"state":"creating"`) ||
		!strings.Contains(body, `"channel":"job1-chat"`) || strings.Contains(body, "slack_channel_id") {
		t.Fatalf("create: %d %s", st, body)
	}
	if got := stub.calls[len(stub.calls)-1]; got != "create:root:job1-chat:jc" {
		t.Fatalf("create call = %q", got)
	}
	stub.created.NotInvited = []bus.BridgeNotInvited{{Identity: "pat@corp.example", Reason: "users_not_found"}}
	stub.created.Bridge.SlackChannelID = "C1"
	stub.created.Bridge.State = bus.BridgeLive
	st, body, _ = e.postBody("root", "/v1/admin/bridges", `{"channel":"job1-chat"}`)
	if st != 200 || !strings.Contains(body, `"not_invited":[{"identity":"pat@corp.example","reason":"users_not_found"}]`) ||
		!strings.Contains(body, `"slack_channel_id":"C1"`) {
		t.Fatalf("create with not_invited: %d %s", st, body)
	}
	if got := stub.calls[len(stub.calls)-1]; got != "create:root:job1-chat:" {
		t.Fatalf("create call = %q", got)
	}
	if st, body, _ = e.postBody("root", "/v1/admin/bridges", `{"channel":"c","extra":1}`); st != 400 {
		t.Fatalf("unknown member: %d %s", st, body)
	}

	// list, get and remove.
	if st, body, _ = e.send("root", http.MethodGet, "/v1/admin/bridges"); st != 200 || !strings.Contains(body, `"bridges":[]`) {
		t.Fatalf("list: %d %s", st, body)
	}
	if st, body, _ = e.send("root", http.MethodGet, "/v1/admin/bridges/job1-chat"); st != 200 || !strings.Contains(body, `"state":"live"`) {
		t.Fatalf("get: %d %s", st, body)
	}
	if st, body, _ = e.send("root", http.MethodDelete, "/v1/admin/bridges/job1-chat"); st != 204 || body != "" {
		t.Fatalf("remove: %d %q", st, body)
	}
	if got := stub.calls[len(stub.calls)-1]; got != "remove:root:job1-chat" {
		t.Fatalf("remove call = %q", got)
	}

	// A stub not_found answers 404 problem+json.
	stub.err = core.E(bus.CodeNotFound, "no bridge for %q", "x")
	st, body, ct := e.send("root", http.MethodGet, "/v1/admin/bridges/x")
	if st != 404 || !strings.HasPrefix(ct, "application/problem+json") || !strings.Contains(body, "no bridge") {
		t.Fatalf("not_found: %d %s %s", st, ct, body)
	}

	// doneScope: an unknown retire mode is a 400 before any handler runs.
	st, body, ct = e.postBody("root", "/v1/admin/scopes/job1/done", `{"retire":"later"}`)
	if st != 400 || !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("retire later: %d %s %s", st, ct, body)
	}
	if st, body, _ = e.postBody("root", "/v1/admin/scopes/job1/done", `{}`); st != 400 {
		t.Fatalf("no retire: %d %s", st, body)
	}
}

func TestBridgesUnconfiguredWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/admin/bridges", `{"channel":"c"}`},
		{http.MethodGet, "/v1/admin/bridges", ""},
		{http.MethodGet, "/v1/admin/bridges/c", ""},
		{http.MethodDelete, "/v1/admin/bridges/c", ""},
	} {
		var st int
		var body, ct string
		if c.body != "" {
			st, body, ct = e.postBody("root", c.path, c.body)
		} else {
			st, body, ct = e.send("root", c.method, c.path)
		}
		if st != 403 || !strings.HasPrefix(ct, "application/problem+json") || !strings.Contains(body, bus.BridgeNotConfigured) {
			t.Errorf("%s %s: %d %s %s", c.method, c.path, st, ct, body)
		}
	}
}

func TestCreateAgentIdentityWire(t *testing.T) {
	e := newEnv(t, envOptions{})
	e.identity("root", true)
	e.identity("pat", false)
	const path = "/v1/admin/identities"
	if st, body, _ := e.postBody("root", path, `{"name":"w","extra":1}`); st != 400 {
		t.Fatalf("unknown member: %d %s", st, body)
	}
	st, body, _ := e.postBody("root", path, `{"name":"w"}`)
	if st != 200 || !strings.Contains(body, `"identity":"w"`) || !strings.Contains(body, `"created":true`) ||
		!strings.Contains(body, `"enrollment_token":"`) || strings.Contains(body, `"scope"`) {
		t.Fatalf("create: %d %s", st, body)
	}
	if st, _, _ = e.postBody("pat", path, `{"name":"x"}`); st != 403 {
		t.Fatalf("as pat = %d, want 403", st)
	}
	if e.hasIdentity("x") {
		t.Fatal("a refused request created the identity")
	}
}
