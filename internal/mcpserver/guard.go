package mcpserver

import (
	"fmt"
	"path/filepath"

	"github.com/ericfitz/agentmesh/internal/client"
)

// IdentityGuard is Decision 16: an agent's MCP server never runs as the OS
// login name, which belongs to the human (the TUI's identity). command names
// the command the message tells the operator to configure (`agentmesh mcp` or
// `agentmesh-mcponly serve`). With no known login name there is nothing to
// compare, and a resolution failure is left for the server to report.
// open note: mcp-only-client 6 (one guard for both binaries).
func IdentityGuard(o client.Options, login, command string) error {
	if login == "" {
		return nil
	}
	r, err := client.Resolve(o)
	if err != nil || r.Identity != login {
		return nil
	}
	return fmt.Errorf("%s: identity %q is the OS login name, which is the human's; set the `identity` of profile %q in %s to another name, or pass -identity to `%s` in the harness's MCP config", command, r.Identity, r.Profile.Name, filepath.Join(r.ConfigDir, "config.json"), command)
}
