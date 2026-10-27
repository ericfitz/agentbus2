package mcpserver_test

import (
	"os"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/storetest"
)

// TestRunStoreLogsToMCPLog (issue #20): in agentbus2 mcp the Store's
// read-normalization error goes to <data dir>/logs/mcp-<pid>.log, never to
// stderr.
func TestRunStoreLogsToMCPLog(t *testing.T) {
	e := newTestEnv(t)
	// pat (the profile's identity) is an administrator holding a scope: every
	// call loads the principal.
	storetest.ScopedAdminRaw(t, profile.SQLitePath(e.Config.Profiles["default"], e.DataDir), "pat", "acme")

	r, stderr := helperSession(t, e)
	r.must(t, "register", nil)
	r.must(t, "list_channels", nil)

	files := logFiles(t, e)
	if len(files) != 1 {
		t.Fatalf("log files while running: %v", files)
	}
	var text string
	waitFor(t, "the normalization error in the MCP log", func() bool {
		b, err := os.ReadFile(files[0])
		text = string(b)
		return err == nil && strings.Contains(text, storetest.NormalizationError)
	})
	if !strings.Contains(text, `"identity":"pat"`) {
		t.Errorf("the MCP log's normalization error does not name pat: %s", text)
	}
	if err := r.CS.Close(); err != nil {
		t.Logf("close: %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("the server wrote to stderr: %q", stderr.String())
	}
}
