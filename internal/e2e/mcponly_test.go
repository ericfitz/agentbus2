//go:build !windows

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentmesh/internal/testkit"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentmesh/internal/auth/keys"
	"github.com/ericfitz/agentmesh/internal/mcpcli"
	"github.com/ericfitz/agentmesh/internal/mcpserver"
)

// runOnly runs agentmesh-mcponly to completion under home.
func runOnly(home string, args ...string) result {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, mcpOnlyBin, args...)
	cmd.Env = environFor(home)
	cmd.Dir = home // not the repository: its .local/agentmesh.json would select a profile
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
func connectOnly(t *testing.T, home, command string, args ...string) (*mcp.ClientSession, *testkit.LockedBuffer) {
	t.Helper()
	cmd := exec.Command(command, args...)
	cmd.Env = environFor(home)
	cmd.Dir = home
	stderr := &testkit.LockedBuffer{}
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
// agentmesh-mcponly over TCP against the e2e server in enrollment mode. The
// leaf names are pinned by scripts/m10-exit-checks.sh.
func TestMCPOnly(t *testing.T) {
	url := fx.serverURL()
	home := t.TempDir()

	t.Run("1-check-reach", func(t *testing.T) {
		r := runOnly(home, "check", "-server", url)
		if r.code != 0 || !strings.Contains(r.stdout, "ok   reach: "+url) || !strings.Contains(r.stdout, "ok   health: ") ||
			!strings.Contains(r.stdout, "next: agentmesh-mcponly register -server "+url+" -identity NAME") {
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
		e := doc.MCPServers["agentmesh"]
		// Ruling 24: the path as the OS reports it, symlinks not resolved (a
		// Homebrew Cellar path would not survive an upgrade).
		want := mcpOnlyBin
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

// helpExample is one `$` line of a help text with the lines and exit code it
// expects (open note: mcp-only-client 12).
type helpExample struct {
	line  string
	wants []string
	exit  int
}

// parseHelpExamples reads the example grammar of internal/mcpcli/help.go:
// `  $ COMMAND`, then `    > OUTPUT` lines, then an optional `    (exit N)`.
// A line that looks like an example but breaks the grammar fails the test.
func parseHelpExamples(t *testing.T, text string) []helpExample {
	t.Helper()
	exitRE := regexp.MustCompile(`^    \(exit ([1-9][0-9]*)\)$`)
	var out []helpExample
	for _, l := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "  $ "):
			out = append(out, helpExample{line: strings.TrimSpace(l[4:])})
		case strings.HasPrefix(l, "    > "):
			if len(out) == 0 {
				t.Fatalf("expected-output line before any example: %q", l)
			}
			e := &out[len(out)-1]
			e.wants = append(e.wants, l[6:])
		case exitRE.MatchString(l):
			if len(out) == 0 {
				t.Fatalf("exit line before any example: %q", l)
			}
			n, _ := strconv.Atoi(exitRE.FindStringSubmatch(l)[1])
			out[len(out)-1].exit = n
		case strings.HasPrefix(trimmed, "$") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "(exit"):
			t.Fatalf("malformed example line: %q", l)
		}
	}
	return out
}

// helpPattern turns an expected-output line into an anchored regexp: a <...>
// token matches any non-empty text and everything else is literal.
func helpPattern(want string) *regexp.Regexp {
	tok := regexp.MustCompile(`<[^>]*>`)
	var b strings.Builder
	b.WriteString("^")
	last := 0
	for _, loc := range tok.FindAllStringIndex(want, -1) {
		b.WriteString(regexp.QuoteMeta(want[last:loc[0]]))
		b.WriteString(".+")
		last = loc[1]
	}
	b.WriteString(regexp.QuoteMeta(want[last:]))
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func writeFreshKey(t *testing.T, path string) {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := keys.EncodePrivatePEM(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestHelpExamples runs every `$` line of the guided help against the e2e
// server, each leaf in a fresh home, and requires the exit code and each `>`
// line the text states. The leaf names are pinned by
// scripts/m10-exit-checks.sh. The commands run with the home as their working
// directory, so the repository's .local/agentmesh.json selects no profile.
func TestHelpExamples(t *testing.T) {
	url := fx.serverURL()
	for _, leaf := range []string{"overview", "connect", "register", "configure", "verify", "troubleshoot"} {
		t.Run(leaf, func(t *testing.T) {
			topic := leaf
			if leaf == "overview" {
				topic = ""
			}
			text, ok := mcpcli.Help(topic)
			if !ok {
				t.Fatalf("no help text for %q", topic)
			}
			examples := parseHelpExamples(t, text)
			if len(examples) == 0 {
				t.Fatalf("%s has no example", leaf)
			}
			home := t.TempDir()
			identity := "help-" + leaf
			tokenFile := filepath.Join(home, "enrollment-token")
			freshToken := func() {
				if err := os.WriteFile(tokenFile, []byte(newEnrollmentToken(t)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeFreshKey(t, filepath.Join(home, "agent-key.pem"))
			if leaf == "configure" || leaf == "verify" {
				freshToken()
				r := runOnly(home, "register", "-server", url, "-identity", identity, "-enrollment-token-file", tokenFile)
				if r.code != 0 {
					t.Fatalf("setup register: exit %d, stdout %q, stderr %q", r.code, r.stdout, r.err)
				}
			}
			// The unreachable-server example names a port that was listening a moment ago.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			closed := ln.Addr().String()
			_ = ln.Close()
			subst := strings.NewReplacer("https://agentbus.example.com", url, "my-agent", identity, "127.0.0.1:1", closed)
			for _, ex := range examples {
				line := subst.Replace(ex.line)
				if strings.Contains(ex.line, "./enrollment-token") {
					freshToken() // a token registers once; every use of the file gets a new one
				}
				fields := strings.Fields(line)
				if len(fields) < 2 || fields[0] != "agentmesh-mcponly" {
					t.Fatalf("example %q does not start with agentmesh-mcponly", ex.line)
				}
				r := runOnly(home, fields[1:]...)
				if r.code != ex.exit {
					t.Errorf("$ %s: exit %d, want %d (stdout %q, stderr %q)", line, r.code, ex.exit, r.stdout, r.err)
					continue
				}
				got := strings.Split(r.stdout+"\n"+r.err, "\n")
				for _, w := range ex.wants {
					re := helpPattern(subst.Replace(w))
					if !slices.ContainsFunc(got, re.MatchString) {
						t.Errorf("$ %s: no output line matches %q\nstdout %q\nstderr %q", line, w, r.stdout, r.err)
					}
				}
			}
		})
	}
}

// TestHelpPattern pins the matcher of the help examples: anchored, <...> is
// any non-empty text and every other character is literal.
func TestHelpPattern(t *testing.T) {
	for _, c := range []struct {
		want, line string
		match      bool
	}{
		{"ok   reach: <x>", "ok   reach: http://h", true},
		{"ok   reach: <x>", "ok   reach: ", false},
		{"ok   reach: <x>", "ok   reach:", false},
		{"ok", "xok", false},
		{"ok", "okx", false},
		{"a.c (b)", "a.c (b)", true},
		{"a.c (b)", "abc (b)", false},
		{"a+b[0]", "a+b[0]", true},
		{"a+b[0]", "aab0", false},
		{"x <a> y <b> z", "x 1 y 2 z", true},
		{"x <a> y", "x  y", false},
	} {
		if got := helpPattern(c.want).MatchString(c.line); got != c.match {
			t.Errorf("helpPattern(%q) on %q = %v, want %v", c.want, c.line, got, c.match)
		}
	}
}
