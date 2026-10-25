package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/core/identities"
)

// adminEnv makes the default profile's identity (pat) an administrator.
func adminEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	pat := e.conn(t, "pat")
	if err := (identities.Service{Core: pat.Core}).MakeAdmin(context.Background(), "pat", true); err != nil {
		t.Fatal(err)
	}
	return e
}

type cliRun func(args ...string) (int, string, string)

func runner(e *testEnv, f runFunc) cliRun {
	return func(args ...string) (int, string, string) {
		var o, er bytes.Buffer
		code := f(context.Background(), args, e.Environ, strings.NewReader(""), &o, &er)
		return code, o.String(), er.String()
	}
}

func TestScopesCommands(t *testing.T) {
	e := adminEnv(t)
	run := runner(e, RunScopes)
	chans := runner(e, RunChannels)

	code, out, er := run("create", "job1")
	if code != 0 {
		t.Fatalf("create: %d %s", code, er)
	}
	var sc struct {
		Name, State string
		Channels    []string
	}
	if err := json.Unmarshal([]byte(out), &sc); err != nil || sc.Name != "job1" || sc.State != "open" {
		t.Fatalf("create out %q: %v", out, err)
	}
	if code, _, er := chans("create", "-scope", "job1", "job1-chat"); code != 0 {
		t.Fatalf("channel: %d %s", code, er)
	}
	code, out, er = run("get", "job1")
	if code != 0 || !strings.Contains(out, `"job1-chat"`) {
		t.Fatalf("get: %d %q %s", code, out, er)
	}
	code, out, _ = run("list")
	var list []struct{ Name string }
	if code != 0 || json.Unmarshal([]byte(out), &list) != nil || len(list) != 1 || list[0].Name != "job1" {
		t.Fatalf("list: %d %q", code, out)
	}

	for _, args := range [][]string{
		{"done", "-retire", "later", "job1"},
		{"done", "job1"},
		{"frob"},
		{"create"},
		{"get"},
		{"done", "-retire", "normal"},
	} {
		code, _, er := run(args...)
		if code != 2 {
			t.Fatalf("%v: exit %d, want 2 (%s)", args, code, er)
		}
		if args[0] == "done" && len(args) > 2 && args[2] == "later" && !strings.Contains(er, "with_bridge, normal") {
			t.Fatalf("done -retire later: %q does not name the allowed values", er)
		}
	}

	code, out, er = run("done", "-retire", "normal", "job1")
	if code != 0 || !strings.Contains(out, `"state": "done"`) {
		t.Fatalf("done: %d %q %s", code, out, er)
	}
	if code, _, er := run("get", "nosuch"); code != 1 || !strings.Contains(er, "agentbus2: not_found:") {
		t.Fatalf("get nosuch: %d %q", code, er)
	}
}

func TestIdentitiesCreateCommand(t *testing.T) {
	e := adminEnv(t)
	scopes := runner(e, RunScopes)
	ids := runner(e, RunIdentities)
	if code, _, er := scopes("create", "job1"); code != 0 {
		t.Fatalf("scope: %d %s", code, er)
	}
	code, out, er := ids("create", "-scope", "job1", "agent1")
	if code != 0 {
		t.Fatalf("create: %d %s", code, er)
	}
	var got struct {
		Identity        string `json:"identity"`
		Scope           string `json:"scope"`
		EnrollmentToken string `json:"enrollment_token"`
		ExpiresAt       int64  `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Identity != "agent1" || got.Scope != "job1" || got.EnrollmentToken == "" || got.ExpiresAt == 0 {
		t.Fatalf("out %q: %+v %v", out, got, err)
	}
	code, out2, er := ids("create", "-scope", "job1", "agent1")
	if code != 0 {
		t.Fatalf("second create: %d %s", code, er)
	}
	var again struct {
		Created         bool   `json:"created"`
		EnrollmentToken string `json:"enrollment_token"`
	}
	if err := json.Unmarshal([]byte(out2), &again); err != nil || again.Created || again.EnrollmentToken == "" || again.EnrollmentToken == got.EnrollmentToken {
		t.Fatalf("second create out %q: %+v %v (want created false and a new token)", out2, again, err)
	}
	if code, _, er := ids("create", "-scope", "nosuch", "agent2"); code != 1 || !strings.Contains(er, "agentbus2: not_found:") {
		t.Fatalf("unknown scope: %d %q", code, er)
	}
	for _, args := range [][]string{{"create"}, {"frob"}} {
		if code, _, _ := ids(args...); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestChannelsCreateScope(t *testing.T) {
	e := adminEnv(t)
	if code, _, er := runner(e, RunScopes)("create", "job1"); code != 0 {
		t.Fatalf("scope: %d %s", code, er)
	}
	code, out, er := runner(e, RunChannels)("create", "-scope", "job1", "job1-chat")
	if code != 0 || !strings.Contains(out, `"scope": "job1"`) {
		t.Fatalf("create: %d %q %s", code, out, er)
	}
	// An unscoped channel's output is unchanged.
	if code, out, _ := runner(e, RunChannels)("create", "plain"); code != 0 || strings.Contains(out, "scope") {
		t.Fatalf("plain: %d %q", code, out)
	}
}

// A non-administrator is refused by every scopes and identities action with
// forbidden (exit 1) and prints nothing. The admin runs first so the
// scope exists and the refusal is about the caller, not a missing scope.
func TestScopesAndIdentitiesNonAdmin(t *testing.T) {
	e := adminEnv(t)
	if code, _, er := runner(e, RunScopes)("create", "job1"); code != 0 {
		t.Fatalf("scope: %d %s", code, er)
	}
	if err := (identities.Service{Core: e.conn(t, "pat").Core}).MakeAdmin(context.Background(), "pat", false); err != nil {
		t.Fatal(err)
	}
	scopes := runner(e, RunScopes)
	ids := runner(e, RunIdentities)
	for _, tc := range []struct {
		run  cliRun
		args []string
	}{
		{scopes, []string{"create", "job2"}},
		{scopes, []string{"list"}},
		{scopes, []string{"get", "job1"}},
		{scopes, []string{"done", "-retire", "normal", "job1"}},
		{ids, []string{"create", "agent1"}},
		{ids, []string{"create", "-scope", "job1", "agent1"}},
	} {
		code, out, er := tc.run(tc.args...)
		if code != 1 || !strings.Contains(er, "agentbus2: forbidden:") || out != "" {
			t.Errorf("%v: exit %d out %q err %q, want exit 1 forbidden and no output", tc.args, code, out, er)
		}
	}
	// The admin still sees the one scope.
	if err := (identities.Service{Core: e.conn(t, "pat").Core}).MakeAdmin(context.Background(), "pat", true); err != nil {
		t.Fatal(err)
	}
	_, out, _ := scopes("list")
	var list []struct{ Name, State string }
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 || list[0].Name != "job1" || list[0].State != "open" {
		t.Fatalf("scopes after refusals: %q %v", out, err)
	}
}
