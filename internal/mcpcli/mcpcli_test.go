package mcpcli

import (
	"bytes"
	"context"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ericfitz/agentmesh/internal/mcpserver"
	"github.com/ericfitz/agentmesh/internal/version"
)

func run(args []string, environ []string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Main(context.Background(), args, environ, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func TestMainDispatch(t *testing.T) {
	code, out, errs := run([]string{"bogus"}, nil)
	if code != 2 || out != "" || !strings.Contains(errs, `agentmesh-mcponly: unknown command "bogus"`) {
		t.Errorf("bogus: code %d, stdout %q, stderr %q", code, out, errs)
	}
	overview, _ := Help("")
	code, out, errs = run(nil, nil)
	if code != 2 || out != "" || errs != overview {
		t.Errorf("no args: code %d, stdout %q, stderr %q; want the overview on stderr", code, out, errs)
	}
	for _, a := range []string{"help", "-h", "--help"} {
		code, out, errs = run([]string{a}, nil)
		if code != 0 || out != overview || errs != "" || firstLine(out) != usageLine {
			t.Errorf("%s: code %d, stdout %q, stderr %q", a, code, out, errs)
		}
		verify, _ := Help("verify")
		code, out, errs = run([]string{a, "verify"}, nil)
		if code != 0 || out != verify || errs != "" {
			t.Errorf("%s verify: code %d, stdout %q, stderr %q", a, code, out, errs)
		}
		for _, bad := range []string{"bogus", "", "overview"} {
			code, out, errs = run([]string{a, bad}, nil)
			want := "agentmesh-mcponly help: unknown topic " + strconv.Quote(bad) + "; topics: connect, register, configure, verify, protocol, troubleshoot\n"
			if code != 2 || out != "" || errs != want {
				t.Errorf("%s %q: code %d, stdout %q, stderr %q", a, bad, code, out, errs)
			}
		}
	}
	for _, a := range []string{"version", "-v", "--version"} {
		code, out, errs = run([]string{a}, nil)
		if code != 0 || out != version.Version+"\n" || errs != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", a, code, out, errs)
		}
	}
}

func TestHelpVersionRejectExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"help", "verify", "x"}, {"-h", "verify", "x"}, {"--help", "bogus", "x"}, {"version", "x"}, {"-v", "x"}, {"--version", "x"}} {
		code, out, errs := run(args, nil)
		if code != 2 || out != "" || !strings.Contains(errs, "unexpected argument") || !strings.Contains(errs, `"x"`) || !strings.Contains(errs, usageLine) {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
}

func TestUnknownFlagsExit2(t *testing.T) {
	for _, c := range []string{"serve", "register", "check", "config"} {
		code, out, errs := run([]string{c, "-bogus"}, nil)
		if code != 2 || out != "" || !strings.Contains(errs, "bogus") {
			t.Errorf("%s -bogus: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
}

// open note: mcp-only-client 14
func TestOnlyMCPCommands(t *testing.T) {
	want := []string{"check", "config", "help", "register", "serve", "version"}
	if got := commandNames(); !slices.Equal(got, want) {
		t.Fatalf("commandNames = %v, want %v", got, want)
	}
}

func TestServeOptions(t *testing.T) {
	env := []string{"AGENTMESH_SERVER=http://h:1", "AGENTMESH_CREDENTIAL=k.pem"}
	o, code := serveOptions(nil, env, &bytes.Buffer{})
	if code != -1 {
		t.Fatalf("code %d", code)
	}
	if o.Edition != mcpserver.EditionMCPOnly {
		t.Errorf("Edition = %v", o.Edition)
	}
	if o.Client.Local != nil {
		t.Error("Client.Local is set: the MCP-only binary has no local mode")
	}
	if o.Client.Server != "http://h:1" {
		t.Errorf("Server = %q", o.Client.Server)
	}
	if !strings.HasSuffix(o.Client.Credential, "k.pem") || !isAbs(o.Client.Credential) {
		t.Errorf("Credential = %q, want an absolute path ending in k.pem", o.Client.Credential)
	}

	o, code = serveOptions([]string{"-server", "http://h:2"}, env, &bytes.Buffer{})
	if code != -1 || o.Client.Server != "http://h:2" {
		t.Errorf("-server over the variable: code %d, Server %q", code, o.Client.Server)
	}

	var errb bytes.Buffer
	if _, code = serveOptions([]string{"extra"}, nil, &errb); code != 2 || !strings.Contains(errb.String(), `unexpected argument "extra"`) {
		t.Errorf("positional: code %d, stderr %q", code, errb.String())
	}
}

func TestServeIdentityGuard(t *testing.T) {
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	t.Cleanup(func() { currentUser = prev })
	dir := t.TempDir()
	t.Chdir(dir) // no .local/agentmesh.json selects a profile
	env := []string{"HOME=" + dir, "AGENTMESH_CONFIG_DIR=" + dir, "AGENTMESH_DATA_DIR=" + dir}
	code, out, errs := run([]string{"serve", "-identity", "eric"}, env)
	if code != 1 || out != "" || !strings.Contains(errs, "agentmesh-mcponly serve") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}
