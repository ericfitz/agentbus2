package mcpcli

import (
	"bytes"
	"context"
	"os/user"
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/version"
)

func run(args []string, environ []string) (code int, stdout, stderr string) {
	var out, errb bytes.Buffer
	code = Main(context.Background(), args, environ, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func firstLine(s string) string { return strings.SplitN(s, "\n", 2)[0] }

func TestMainDispatch(t *testing.T) {
	code, out, errs := run(nil, nil)
	if code != 2 || out != "" || firstLine(errs) != usageLine {
		t.Errorf("no args: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = run([]string{"bogus"}, nil)
	if code != 2 || out != "" || !strings.Contains(errs, `agentbus2-mcponly: unknown command "bogus"`) {
		t.Errorf("bogus: code %d, stdout %q, stderr %q", code, out, errs)
	}
	for _, a := range []string{"help", "-h", "--help"} {
		code, out, errs = run([]string{a}, nil)
		if code != 0 || firstLine(out) != usageLine || errs != "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", a, code, out, errs)
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
	for _, args := range [][]string{{"help", "x"}, {"-h", "x"}, {"--help", "x"}, {"version", "x"}, {"-v", "x"}, {"--version", "x"}} {
		code, out, errs := run(args, nil)
		if code != 2 || out != "" || !strings.Contains(errs, "unexpected argument") || !strings.Contains(errs, usageLine) {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, out, errs)
		}
	}
}

func TestUnknownFlagsExit2(t *testing.T) {
	for _, c := range []string{"serve", "register"} {
		code, out, errs := run([]string{c, "-bogus"}, nil)
		if code != 2 || out != "" || !strings.Contains(errs, "bogus") {
			t.Errorf("%s -bogus: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
}

// open note: mcp-only-client 14
func TestOnlyMCPCommands(t *testing.T) {
	want := []string{"help", "register", "serve", "version"}
	if got := commandNames(); !slices.Equal(got, want) {
		t.Fatalf("commandNames = %v, want %v", got, want)
	}
}

func TestServeOptions(t *testing.T) {
	env := []string{"AGENTBUS2_SERVER=http://h:1", "AGENTBUS2_CREDENTIAL=k.pem"}
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
	t.Chdir(dir) // no .local/agentbus2.json selects a profile
	env := []string{"HOME=" + dir, "AGENTBUS2_CONFIG_DIR=" + dir, "AGENTBUS2_DATA_DIR=" + dir}
	code, out, errs := run([]string{"serve", "-identity", "eric"}, env)
	if code != 1 || out != "" || !strings.Contains(errs, "agentbus2-mcponly serve") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}
