package cli

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/profile"
)

func TestSessionStartHook(t *testing.T) {
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	t.Cleanup(func() { currentUser = prev })
	e := newTestEnv(t)
	ctx := context.Background()
	run := func(environ []string, stdin string) (int, string) {
		var so, se syncBuf
		code := RunSessionStartHook(ctx, nil, environ, strings.NewReader(stdin), &so, &se)
		return code, so.String()
	}

	// No identity resolves: silent, exit 0, one line in hook.log.
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeLocal})
	if code, out := run(e.Environ, `{"cwd":""}`); code != 0 || out != "" {
		t.Fatalf("no identity: exit %d stdout %q", code, out)
	}

	// An identity: the register steps, naming the profile and identity.
	withID := append(append([]string(nil), e.Environ...), "AGENTBUS2_IDENTITY=pat")
	code, out := run(withID, `{"cwd":"","source":"startup"}`)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{`profile "default"`, `identity "pat"`, "without waiting to be asked", mcpserver.RegisterSteps, "using-agentbus2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q: %s", want, out)
		}
	}

	// The OS login name: the MCP server refuses it (Decision 16), so silent.
	asLogin := append(append([]string(nil), e.Environ...), "AGENTBUS2_IDENTITY=eric")
	if code, out := run(asLogin, `{"cwd":""}`); code != 0 || out != "" {
		t.Fatalf("OS login identity: exit %d stdout %q", code, out)
	}

	// Bad or empty input: silent, exit 0.
	for _, in := range []string{"not json", "", "[]"} {
		if code, out := run(withID, in); code != 0 || out != "" {
			t.Errorf("input %q: exit %d stdout %q", in, code, out)
		}
	}
	if log, err := os.ReadFile(filepath.Join(e.DataDir, "logs", "hook.log")); err != nil || !strings.Contains(string(log), " resolve: ") {
		t.Fatalf("hook.log lacks the resolve line: %v\n%s", err, log)
	}
}
