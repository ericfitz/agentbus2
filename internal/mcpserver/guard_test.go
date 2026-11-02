package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// TestIdentityGuard is Decision 16 for every agent MCP server.
// open note: mcp-only-client 6 (the guard is shared; the message names the
// command each binary runs).
func TestIdentityGuard(t *testing.T) {
	e := newTestEnv(t)
	const cmd = "agentbus2-mcponly serve"
	err := mcpserver.IdentityGuard(client.Options{Environ: e.Environ, Cwd: e.Home, Identity: "eric"}, "eric", cmd)
	if err == nil {
		t.Fatal("login eric, identity eric: want an error")
	}
	if n := strings.Count(err.Error(), cmd); n != 2 {
		t.Errorf("message names %q %d times, want 2: %v", cmd, n, err)
	}
	for _, w := range []string{`"eric"`, `profile "default"`, "-identity"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("message lacks %q: %v", w, err)
		}
	}
	if err := mcpserver.IdentityGuard(client.Options{Environ: e.Environ, Cwd: e.Home, Identity: "eric"}, "", cmd); err != nil {
		t.Errorf("empty login: %v", err)
	}
	if err := mcpserver.IdentityGuard(client.Options{Environ: e.Environ, Cwd: e.Home, Identity: "pat"}, "eric", cmd); err != nil {
		t.Errorf("identity pat: %v", err)
	}
	// An unresolvable profile is left for the server to report.
	if err := mcpserver.IdentityGuard(client.Options{Environ: e.Environ, Cwd: e.Home, Profile: "nope", Identity: "eric"}, "eric", cmd); err != nil {
		t.Errorf("unresolvable profile: %v", err)
	}
}
