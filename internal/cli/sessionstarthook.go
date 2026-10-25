package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// sessionStartHookInput is the part of a SessionStart hook's stdin that
// RunSessionStartHook reads (Claude Code and Codex both send cwd).
type sessionStartHookInput struct {
	Cwd string `json:"cwd"`
}

func sessionStartHookWork(environ []string, stdin io.Reader) hookResult {
	var hi sessionStartHookInput
	if err := json.NewDecoder(stdin).Decode(&hi); err != nil {
		return hookResult{stage: "decode", err: err}
	}
	// No bus call and no state file: the session does not exist yet. The
	// profile and identity resolve the way `agentbus2 mcp` resolves them, so
	// the hook stays silent wherever the MCP server's tools would refuse.
	r, err := client.Resolve(client.Options{Environ: environ, Cwd: hi.Cwd})
	if err != nil {
		return hookResult{stage: "resolve", err: err}
	}
	if isOSLogin(r.Identity) {
		return hookResult{} // Decision 16: the MCP server refuses to start
	}
	text := fmt.Sprintf("Agentbus2 is set up for this session (profile %q, identity %q). "+
		"Do this now, without waiting to be asked. %s The using-agentbus2 skill is the full protocol.\n",
		r.Profile.Name, r.Identity, mcpserver.RegisterSteps)
	return hookResult{out: []byte(text)}
}

// RunSessionStartHook is the SessionStart hook of Claude Code and Codex (Eric,
// 2026-10-24; ADR 0003 session-start addendum), ported from v1
// internal/cli/identity.go identityLine without the identity it carried
// (Decision 14 stands): it prints the register steps as plain text, which both
// harnesses add to the agent's context. It prints nothing when the profile or
// identity does not resolve or the identity is the OS login name. Grok ignores
// SessionStart stdout; grokRule does this job there. It always exits 0 and
// falls open within hookBudget.
func RunSessionStartHook(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHook(ctx, environ, stdout, func(context.Context) hookResult {
		return sessionStartHookWork(environ, stdin)
	})
}
