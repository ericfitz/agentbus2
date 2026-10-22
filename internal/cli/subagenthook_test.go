package cli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

func TestSubagentHookContext(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	sess, err := e.conn(t, "pat").Bus.Open(ctx, bus.OpenSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	run := func(stdin string) (int, string) {
		var so, se syncBuf
		code := RunSubagentHook(ctx, nil, e.Environ, strings.NewReader(stdin), &so, &se)
		return code, so.String()
	}

	// No state file for the harness: nothing.
	if code, out := run(`{"agent_type":"Code Reviewer"}`); code != 0 || out != "" {
		t.Fatalf("no state file: exit %d stdout %q", code, out)
	}

	e.writeState(t, sessionstate.File{Session: sess.ID, Identity: "pat", Profile: "default", DisplayName: sess.DisplayName, ResumeToken: sess.ResumeToken,
		HarnessPID: testHarness.Pid, HarnessStart: testHarness.Start, Primary: true, OpenedAt: 1})
	code, out := run(`{"cwd":"","agent_type":"Code Reviewer"}`)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var res struct {
		H map[string]string `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.H["hookEventName"] != "SubagentStart" {
		t.Fatalf("shape: %q (%v)", out, err)
	}
	c := res.H["additionalContext"]
	for _, want := range []string{"register", "only if your prompt asks you to use agentbus2", "using-agentbus2", "receive", "Do not start wait or watch"} {
		if !strings.Contains(c, want) {
			t.Errorf("context lacks %q: %s", want, c)
		}
	}
	if len(c) >= 300 {
		t.Errorf("context is %d characters, want under 300: %s", len(c), c)
	}
	if strings.Contains(c, "parent=") {
		t.Errorf("context names a parent: %s", c)
	}
	m := regexp.MustCompile(`hint="([^"]*)"`).FindStringSubmatch(c)
	if m == nil || !regexp.MustCompile(`^code-reviewer-[0-9a-f]{4}$`).MatchString(m[1]) {
		t.Errorf("hint %v in %s", m, c)
	}

	// Bad or empty input: silent, exit 0.
	for _, in := range []string{"not json", "", "[]"} {
		if code, out := run(in); code != 0 || out != "" {
			t.Errorf("input %q: exit %d stdout %q", in, code, out)
		}
	}
}

func TestSubagentName(t *testing.T) {
	for in, re := range map[string]string{
		"general-purpose":       `^general-purpose-[0-9a-f]{4}$`,
		"Code Reviewer":         `^code-reviewer-[0-9a-f]{4}$`,
		"":                      `^subagent-[0-9a-f]{4}$`,
		"!!!":                   `^subagent-[0-9a-f]{4}$`,
		strings.Repeat("a", 60): `^a{40}-[0-9a-f]{4}$`,
	} {
		if got := subagentName(in); !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("subagentName(%q) = %q, want %s", in, got, re)
		}
	}
	if a, b := subagentName("x"), subagentName("x"); a == b {
		t.Errorf("two names for one type are equal: %s", a)
	}
}
