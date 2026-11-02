package mcpcli

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/fileacl"
	"github.com/ericfitz/agentbus2/internal/version"
)

// writeFreshKey writes a new 0600 EdDSA key file and returns its path.
func writeFreshKey(t *testing.T, mode os.FileMode) string {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := keys.EncodePrivatePEM(k)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pemBytes, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	if mode == 0o600 {
		if err := fileacl.OwnerOnly(path); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func lines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// open note: mcp-only-client 8
func TestCheckSteps(t *testing.T) {
	t.Chdir(t.TempDir()) // the repository's .local/agentbus2.json must not select a profile
	base := startServer(t)
	environ, _ := testEnviron(t)

	// No identity: reach and health pass, then the next step is printed.
	code, out, errs := run([]string{"check", "-server", base}, environ)
	got := lines(out)
	if code != 0 || errs != "" || len(got) != 3 ||
		!strings.HasPrefix(got[0], "ok   reach: "+base+" (server ") || !strings.Contains(got[0], ", protocol "+version.Protocol+")") ||
		!strings.HasPrefix(got[1], "ok   health: ") || !strings.HasSuffix(got[1], " (probe tier)") ||
		got[2] != "next: agentbus2-mcponly register -server "+base+" -identity NAME" {
		t.Errorf("no identity: code %d, stdout %q, stderr %q", code, out, errs)
	}

	// After register: six ok lines.
	if code, out, errs = run([]string{"register", "-server", base, "-identity", "pat"}, environ); code != 0 {
		t.Fatalf("register: %d %q %q", code, out, errs)
	}
	code, out, errs = run([]string{"check"}, environ)
	got = lines(out)
	steps := []string{"reach", "health", "identity", "credential", "token", "access"}
	if code != 0 || errs != "" || len(got) != len(steps) {
		t.Fatalf("registered: code %d, stdout %q, stderr %q", code, out, errs)
	}
	for i, s := range steps {
		if !strings.HasPrefix(got[i], "ok   "+s+": ") {
			t.Errorf("line %d = %q, want ok   %s:", i, got[i], s)
		}
	}
	if !strings.Contains(got[2], "pat (") || !strings.Contains(got[4], "exchanged a bearer for pat") || !strings.Contains(got[5], "health at the identity tier") {
		t.Errorf("details: %q", got)
	}

	// An identity the server does not know, with a fresh key.
	key := writeFreshKey(t, 0o600)
	environ2, _ := testEnviron(t)
	code, out, errs = run([]string{"check", "-server", base, "-identity", "nobody", "-credential", key}, environ2)
	got = lines(out)
	if code != 1 || len(got) != 5 || !strings.HasPrefix(got[4], "FAIL token: unauthenticated:") ||
		!strings.HasSuffix(got[4], "fix: agentbus2-mcponly register -server "+base+" -identity nobody (if nobody already holds another key, an administrator runs agentbus2 server admin reset-credential nobody first)") ||
		strings.TrimSpace(errs) != "agentbus2-mcponly: check: token failed" {
		t.Errorf("unregistered key: code %d, stdout %q, stderr %q", code, out, errs)
	}
	if !strings.HasPrefix(got[3], "ok   credential: "+key) {
		t.Errorf("credential line %q", got[3])
	}

	// A closed port: reach fails and nothing follows.
	code, out, errs = run([]string{"check", "-server", "http://127.0.0.1:1", "-identity", "pat"}, environ2)
	got = lines(out)
	if code != 1 || len(got) != 1 || !strings.HasPrefix(got[0], "FAIL reach: ") ||
		!strings.HasSuffix(got[0], "fix: check the URL and that the server is running, then agentbus2-mcponly check -server http://127.0.0.1:1") ||
		strings.TrimSpace(errs) != "agentbus2-mcponly: check: reach failed" {
		t.Errorf("closed port: code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestCheckNoServerAndNoLocalMode(t *testing.T) {
	environ, _ := testEnviron(t)
	t.Chdir(t.TempDir())
	for _, c := range []string{"check", "config"} {
		code, out, errs := run([]string{c, "-identity", "pat"}, environ)
		if code != 2 || out != "" || !strings.Contains(errs, "has no local mode") {
			t.Errorf("%s with no server: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
	// An undefined named profile with no -server is a usage error too.
	for _, c := range []string{"check", "config"} {
		code, out, errs := run([]string{c, "-profile", "lab", "-identity", "pat"}, environ)
		if code != 2 || out != "" || !strings.Contains(errs, `"lab"`) {
			t.Errorf("%s -profile lab: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
}

func TestCheckRejectsPositionalArguments(t *testing.T) {
	for _, c := range []string{"check", "config"} {
		code, out, errs := run([]string{c, "extra"}, nil)
		if code != 2 || out != "" || !strings.Contains(errs, `unexpected argument "extra"`) {
			t.Errorf("%s extra: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
}

// The token step exchanges every time: a bearer cached for pat by an earlier
// check must not let another, unregistered key pass.
func TestCheckTokenIgnoresCachedBearer(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, _ := testEnviron(t)
	if code, out, errs := run([]string{"register", "-server", base, "-identity", "pat"}, environ); code != 0 {
		t.Fatalf("register: %d %q %q", code, out, errs)
	}
	if code, out, errs := run([]string{"check"}, environ); code != 0 {
		t.Fatalf("first check: %d %q %q", code, out, errs)
	}
	key := writeFreshKey(t, 0o600)
	code, out, errs := run([]string{"check", "-identity", "pat", "-credential", key}, environ)
	if code != 1 || !strings.Contains(out, "FAIL token: unauthenticated:") {
		t.Errorf("other key: code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestCheckNoCredentialFix(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, _ := testEnviron(t)
	code, out, errs := run([]string{"check", "-server", base, "-identity", "pat"}, environ)
	got := lines(out)
	if code != 1 || len(got) != 4 || !strings.HasPrefix(got[3], "FAIL credential: ") ||
		!strings.HasSuffix(got[3], "fix: agentbus2-mcponly register -server "+base+" -identity pat") {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
	}
}

func TestCheckServerDisagreesWithProfile(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, _ := testEnviron(t)
	if code, out, errs := run([]string{"register", "-server", base, "-identity", "pat"}, environ); code != 0 {
		t.Fatalf("register: %d %q %q", code, out, errs)
	}
	for _, c := range []string{"check", "config"} {
		code, out, errs := run([]string{c, "-server", "http://127.0.0.1:1"}, environ)
		if code != 2 || out != "" || !strings.Contains(errs, "disagrees") {
			t.Errorf("%s: code %d, stdout %q, stderr %q", c, code, out, errs)
		}
	}
}

// Decision 16: an identity equal to the OS login name fails the identity step
// with the guard's text, because serve would exit 1 on it.
func TestCheckIdentityGuard(t *testing.T) {
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	t.Cleanup(func() { currentUser = prev })
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, _ := testEnviron(t)
	if code, out, errs := run([]string{"register", "-server", base, "-identity", "pat"}, environ); code != 0 {
		t.Fatalf("register: %d %q %q", code, out, errs)
	}
	currentUser = func() (*user.User, error) { return &user.User{Username: "pat"}, nil }
	code, out, errs := run([]string{"check"}, environ)
	got := lines(out)
	if code != 1 || len(got) != 3 || !strings.HasPrefix(got[2], "FAIL identity: ") ||
		!strings.Contains(got[2], "is the OS login name") || !strings.Contains(got[2], "fix: ") ||
		strings.TrimSpace(errs) != "agentbus2-mcponly: check: identity failed" {
		t.Errorf("code %d, stdout %q, stderr %q", code, out, errs)
	}
	// Another login name passes the step.
	currentUser = func() (*user.User, error) { return &user.User{Username: "eric"}, nil }
	if code, out, errs := run([]string{"check"}, environ); code != 0 {
		t.Errorf("other login: code %d, stdout %q, stderr %q", code, out, errs)
	}
}

// A resolution error gets the fix for its cause, not "pass -identity".
func TestCheckResolveErrorFixes(t *testing.T) {
	t.Chdir(t.TempDir())
	base := startServer(t)
	environ, configDir := testEnviron(t)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"profiles":{"lab":{"mode":"server","server":"` + base + `","credential_process":["true"]}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	key := writeFreshKey(t, 0o600)

	code, out, errs := run([]string{"check", "-profile", "lab", "-identity", "pat", "-credential", key}, environ)
	got := lines(out)
	if code != 1 || len(got) != 3 || !strings.HasPrefix(got[2], "FAIL identity: ") ||
		!strings.Contains(got[2], "fix: drop -credential, or remove credential_process") || strings.Contains(got[2], "pass -identity") {
		t.Errorf("credential_process: code %d, stdout %q, stderr %q", code, out, errs)
	}

	// An unrecognized resolution error points at the troubleshooting topic.
	code, out, errs = run([]string{"check", "-profile", "lab", "-identity", "a/b"}, environ)
	got = lines(out)
	if code != 1 || len(got) != 3 || !strings.HasSuffix(got[2], "fix: agentbus2-mcponly help troubleshoot") {
		t.Errorf("bad identity: code %d, stdout %q, stderr %q", code, out, errs)
	}
}

// The Windows fix names PowerShell and cmd.exe; it is one text for check and
// CheckOwnerOnly.
func TestOwnerOnlyFixWindowsText(t *testing.T) {
	got := fileacl.WindowsFix(`C:\k`)
	if !strings.Contains(got, "${env:USERNAME}") || !strings.Contains(got, "%USERNAME%") {
		t.Errorf("WindowsFix = %q", got)
	}
}
