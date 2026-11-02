//go:build !windows

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// runOnly runs agentbus2-mcponly to completion under home.
func runOnly(home string, args ...string) result {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mcpOnlyBin, args...)
	cmd.Env = environFor(home)
	cmd.Dir = home // not the repository: its .local/agentbus2.json would select a profile
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	return result{code: exitCodeOf(err), stdout: o.String(), err: e.String()}
}

func newEnrollmentToken(t *testing.T) string {
	t.Helper()
	r := run(fx.homeA, "server", "admin", "-sqlite", fx.sqlite, "enrollment", "create")
	m := tokenRE.FindStringSubmatch(r.stdout)
	if r.code != 0 || m == nil {
		t.Fatalf("enrollment create: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
	}
	return m[1]
}

// connectOnly starts `command args...` as an MCP server under home.
func connectOnly(t *testing.T, home, command string, args ...string) (*mcp.ClientSession, *syncBuf) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Env = environFor(home)
	cmd.Dir = home
	stderr := &syncBuf{}
	cmd.Stderr = stderr
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "e2e"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to %s: %v (stderr %q)", command, err, stderr.String())
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, stderr
}

func callOnly(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			text = tc.Text
		}
	}
	return res, text
}

// TestMCPOnly is the MCP-only client's exit (issue #12): the built
// agentbus2-mcponly over TCP against the e2e server in enrollment mode. The
// leaf names are pinned by scripts/m10-exit-checks.sh.
func TestMCPOnly(t *testing.T) {
	url := fx.serverURL()
	home := t.TempDir()

	t.Run("1-check-reach", func(t *testing.T) {
		r := runOnly(home, "check", "-server", url)
		if r.code != 0 || !strings.Contains(r.stdout, "ok   reach: "+url) || !strings.Contains(r.stdout, "ok   health: ") ||
			!strings.Contains(r.stdout, "next: agentbus2-mcponly register -server "+url+" -identity NAME") {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	t.Run("2-register-from-token-file", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(file, []byte(newEnrollmentToken(t)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runOnly(home, "register", "-server", url, "-identity", "quinn", "-enrollment-token-file", file)
		if r.code != 0 || strings.TrimSpace(r.stdout) != "registered quinn at "+url+" (created)" {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
		b, err := os.ReadFile(filepath.Join(configDir(home), "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Profiles map[string]struct {
				Mode, Server, Identity string
			} `json:"profiles"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		if p := cfg.Profiles["default"]; p.Mode != "server" || p.Identity != "quinn" || p.Server != url {
			t.Errorf("config.json default profile = %+v", p)
		}
	})

	t.Run("3-register-again", func(t *testing.T) {
		r := runOnly(home, "register")
		if r.code != 0 || strings.TrimSpace(r.stdout) != "registered quinn at "+url+" (existing key)" {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	t.Run("4-check-identity", func(t *testing.T) {
		r := runOnly(home, "check")
		if r.code != 0 || okLines(r.stdout) != 6 || strings.Contains(r.stdout, "FAIL") {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	var command string
	var args []string
	t.Run("5-config", func(t *testing.T) {
		r := runOnly(home, "config")
		if r.code != 0 {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
		var doc struct {
			MCPServers map[string]struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(strings.SplitN(r.stdout, "\n\n", 2)[0]), &doc); err != nil {
			t.Fatalf("snippet is not JSON: %v\n%s", err, r.stdout)
		}
		e := doc.MCPServers["agentbus2"]
		want, err := filepath.EvalSymlinks(mcpOnlyBin)
		if err != nil {
			t.Fatal(err)
		}
		if e.Command != want || !slices.Equal(e.Args, []string{"serve", "-server", url, "-identity", "quinn"}) {
			t.Fatalf("entry = %+v, want command %s", e, want)
		}
		command, args = e.Command, e.Args
	})

	t.Run("6-serve-stdio", func(t *testing.T) {
		if command == "" {
			t.Skip("leaf 5 produced no command")
		}
		cs, stderr := connectOnly(t, home, command, args...)
		if got := cs.InitializeResult().Instructions; got != mcpserver.MCPOnlyInstructions {
			t.Errorf("Instructions = %q, want MCPOnlyInstructions", got)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tools, err := cs.ListTools(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(tools.Tools) != 26 {
			t.Errorf("%d tools, want 26", len(tools.Tools))
		}
		res, text := callOnly(t, cs, "register", map[string]any{})
		var reg registered
		if res.IsError || json.Unmarshal([]byte(text), &reg) != nil || reg.Session == "" {
			t.Fatalf("register: error %v, %q (stderr %q)", res.IsError, text, stderr.String())
		}
		start := time.Now()
		res, text = callOnly(t, cs, "receive", map[string]any{"session": reg.Session, "wait_seconds": 1})
		if res.IsError || time.Since(start) > 10*time.Second {
			t.Errorf("receive: error %v after %s: %q", res.IsError, time.Since(start), text)
		}
		if res, text = callOnly(t, cs, "close_session", map[string]any{"session": reg.Session}); res.IsError {
			t.Errorf("close_session: %q", text)
		}
	})

	t.Run("7-register-without-token", func(t *testing.T) {
		r := runOnly(t.TempDir(), "register", "-server", url, "-identity", "robin")
		if r.code != 1 || !strings.Contains(r.err, "forbidden") || !strings.Contains(r.err, "requires an enrollment token") {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	t.Run("8-check-unregistered-key", func(t *testing.T) {
		k, err := keys.Generate(keys.EdDSA)
		if err != nil {
			t.Fatal(err)
		}
		pemBytes, err := keys.EncodePrivatePEM(k)
		if err != nil {
			t.Fatal(err)
		}
		key := filepath.Join(t.TempDir(), "ghost.pem")
		if err := os.WriteFile(key, pemBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		r := runOnly(t.TempDir(), "check", "-server", url, "-identity", "ghost", "-credential", key)
		if r.code != 1 || !strings.Contains(r.stdout, "FAIL token: unauthenticated") {
			t.Fatalf("exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
		}
	})

	t.Run("9-no-local-mode", func(t *testing.T) {
		fresh := t.TempDir()
		for _, c := range []string{"check", "config"} {
			r := runOnly(fresh, c, "-identity", "quinn")
			if r.code != 2 || !strings.Contains(r.err, "has no local mode") {
				t.Errorf("%s: exit %d, stdout %q, stderr %q", c, r.code, r.stdout, r.err)
			}
		}
		cs, _ := connectOnly(t, fresh, mcpOnlyBin, "serve", "-identity", "quinn")
		res, text := callOnly(t, cs, "register", map[string]any{})
		if !res.IsError || !strings.Contains(text, "validation") || !strings.Contains(text, "has no local mode") {
			t.Errorf("register tool: error %v, %q", res.IsError, text)
		}
		_ = cs.Close()
		_ = filepath.WalkDir(fresh, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.Contains(d.Name(), ".sqlite") {
				t.Errorf("the no-local-mode run created %s", p)
			}
			return nil
		})
	})
}

func okLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "ok   ") {
			n++
		}
	}
	return n
}
