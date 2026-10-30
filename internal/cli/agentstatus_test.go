package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
)

func runAS(t *testing.T, e *testEnv, args ...string) (int, string, string) {
	t.Helper()
	var so, se syncBuf
	code := RunAgentStatus(context.Background(), args, e.Environ, strings.NewReader(""), &so, &se)
	return code, so.String(), se.String()
}

// open note: agent-status 9
func TestAgentStatusSetLocatesSession(t *testing.T) {
	e := newTestEnv(t)
	code, _, se := runAS(t, e, "set", "planning", "x")
	if code != 2 || !strings.Contains(se, errNoSession.Error()) {
		t.Fatalf("no state file: exit %d stderr %q", code, se)
	}
	sess := parkSession(t, e)
	code, so, se := runAS(t, e, "set", "-problem", "p", "planning", "the plan")
	if code != 0 || se != "" {
		t.Fatalf("set: exit %d stderr %q", code, se)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(so), &got); err != nil {
		t.Fatal(err)
	}
	if got["identity"] != "pat" || got["tag"] != "planning" || got["text"] != "the plan" || got["problem"] != "p" ||
		got["session"] != sess.ID || got["display_name"] != sess.DisplayName || got["revision"] == nil || got["set_at"] == nil {
		t.Fatalf("set output = %v", got)
	}
	// -session with the state file's id, -key replay.
	code, so1, se := runAS(t, e, "set", "-session", sess.ID, "-key", "k1", "idle", "a")
	if code != 0 {
		t.Fatalf("set -session: exit %d stderr %q", code, se)
	}
	_, so2, _ := runAS(t, e, "set", "-session", sess.ID, "-key", "k1", "idle", "a")
	if so1 != so2 {
		t.Errorf("replayed key changed the result:\n%s\n%s", so1, so2)
	}
	if code, _, se := runAS(t, e, "set", "-session", "nope", "idle", "a"); code != 1 || !strings.HasPrefix(se, "agentbus2: ") {
		t.Fatalf("unknown session: exit %d stderr %q", code, se)
	}
}

func TestAgentStatusSetArity(t *testing.T) {
	e := newTestEnv(t)
	parkSession(t, e)
	for _, args := range [][]string{{"set", "planning"}, {"set", "planning", "a", "b"}, {"list", "x"}, {"history"}, {"history", "a", "b"}, {"bogus"}, {}} {
		if code, _, _ := runAS(t, e, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestAgentStatusUnknownTagExit1(t *testing.T) {
	e := newTestEnv(t)
	sess := parkSession(t, e)
	code, so, se := runAS(t, e, "set", "-session", sess.ID, "bogus", "x")
	if code != 1 || so != "" || !strings.HasPrefix(se, `agentbus2: validation: unknown tag "bogus": `) {
		t.Fatalf("exit %d stdout %q stderr %q", code, so, se)
	}
}

func TestAgentStatusListAndHistoryJSON(t *testing.T) {
	e := newTestEnv(t)
	code, so, se := runAS(t, e, "list")
	if code != 0 || strings.TrimSpace(so) != "[]" {
		t.Fatalf("empty list: exit %d stdout %q stderr %q", code, so, se)
	}
	code, so, _ = runAS(t, e, "history", "pat")
	if code != 0 || strings.TrimSpace(so) != "[]" {
		t.Fatalf("empty history: exit %d stdout %q", code, so)
	}
	sess := parkSession(t, e)
	for _, tag := range []string{"planning", "implementing"} {
		if code, _, se := runAS(t, e, "set", "-session", sess.ID, tag, "t-"+tag); code != 0 {
			t.Fatalf("set %s: %d %q", tag, code, se)
		}
	}
	code, so, se = runAS(t, e, "list")
	if code != 0 {
		t.Fatalf("list: %d %q", code, se)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(so), &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %q (%v)", so, err)
	}
	st, _ := list[0]["status"].(map[string]any)
	if list[0]["identity"] != "pat" || list[0]["kind"] != string(bus.IdentityAgent) || list[0]["live"] != true ||
		st["tag"] != "implementing" || st["last_tag"] != "implementing" || st["text"] != "t-implementing" {
		t.Fatalf("list entry = %v", list[0])
	}
	code, so, se = runAS(t, e, "history", "pat")
	if code != 0 {
		t.Fatalf("history: %d %q", code, se)
	}
	var hist []map[string]any
	if err := json.Unmarshal([]byte(so), &hist); err != nil || len(hist) != 2 {
		t.Fatalf("history = %q (%v)", so, err)
	}
	if hist[0]["tag"] != "planning" || hist[1]["tag"] != "implementing" || hist[0]["identity"] != "pat" {
		t.Fatalf("history order = %v", hist)
	}
	if code, _, se := runAS(t, e, "history", "ghost"); code != 1 || !strings.HasPrefix(se, "agentbus2: ") {
		t.Fatalf("history ghost: exit %d stderr %q", code, se)
	}
}

func TestUsageNamesAgentStatus(t *testing.T) {
	if !strings.Contains(Usage, "agent-status") {
		t.Fatalf("Usage does not name agent-status: %s", Usage)
	}
	if _, ok := commands["agent-status"]; !ok {
		t.Fatal("agent-status is not dispatched")
	}
}
