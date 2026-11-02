package mcpcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/fileacl"
)

func testEnviron(t *testing.T) (environ []string, configDir string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "config")
	return []string{"HOME=" + root, "AGENTBUS2_CONFIG_DIR=" + configDir, "AGENTBUS2_DATA_DIR=" + filepath.Join(root, "data")}, configDir
}

// The token never reaches stdout or stderr (plan Review Focus 2).
func TestRegisterTokenNeverEchoed(t *testing.T) {
	base := startServer(t, "-registration", "enrollment")
	environ, _ := testEnviron(t)

	code, out, errs := run([]string{"register", "-server", base, "-identity", "pat"}, environ)
	if code != 1 || !strings.Contains(errs, "forbidden") || !strings.Contains(errs, "requires an enrollment token") || out != "" {
		t.Errorf("no token: code %d, stdout %q, stderr %q", code, out, errs)
	}

	token := "abe_" + strings.Repeat("A", 43)
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs = run([]string{"register", "-server", base, "-identity", "pat", "-enrollment-token-file", file}, environ)
	if code != 1 {
		t.Errorf("wrong token: code %d, stderr %q", code, errs)
	}
	for name, s := range map[string]string{"stdout": out, "stderr": errs} {
		if strings.Contains(s, token) || strings.Contains(s, strings.Repeat("A", 43)) {
			t.Errorf("%s contains the token: %q", name, s)
		}
	}

	// The variable and the flag take the same path.
	code, out, errs = run([]string{"register", "-server", base, "-identity", "pat", "-enrollment-token", token}, environ)
	if code != 1 || strings.Contains(out+errs, token) {
		t.Errorf("flag token: code %d, output %q %q", code, out, errs)
	}
	code, out, errs = run([]string{"register", "-server", base, "-identity", "pat"}, append(environ, "AGENTBUS2_ENROLLMENT_TOKEN="+token))
	if code != 1 || strings.Contains(out+errs, token) {
		t.Errorf("variable token: code %d, output %q %q", code, out, errs)
	}
	// An unreachable server leaks nothing either.
	code, out, errs = run([]string{"register", "-server", "http://127.0.0.1:1", "-identity", "pat", "-enrollment-token", token}, environ)
	if code != 1 || strings.Contains(out+errs, token) {
		t.Errorf("unreachable: code %d, output %q %q", code, out, errs)
	}
}

func TestRegisterUsageErrors(t *testing.T) {
	environ, _ := testEnviron(t)
	cases := map[string][]string{
		"both token flags": {"register", "-server", "http://127.0.0.1:1", "-identity", "pat", "-enrollment-token", "a", "-enrollment-token-file", "b"},
		"bad algorithm":    {"register", "-server", "http://127.0.0.1:1", "-identity", "pat", "-algorithm", "RS256"},
		"positional":       {"register", "extra"},
		"bad token file":   {"register", "-server", "http://127.0.0.1:1", "-identity", "pat", "-enrollment-token-file", filepath.Join(t.TempDir(), "nope")},
	}
	for name, args := range cases {
		if code, out, errs := run(args, environ); code != 2 || out != "" || errs == "" {
			t.Errorf("%s: code %d, stdout %q, stderr %q", name, code, out, errs)
		}
	}
}

func TestRegisterCredentialAndRerun(t *testing.T) {
	base := startServer(t)
	environ, configDir := testEnviron(t)
	s, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	pemb, err := keys.EncodePrivatePEM(s)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "agent-key.pem")
	if err := keys.WriteFile(key, pemb); err != nil {
		t.Fatal(err)
	}
	if err := fileacl.OwnerOnly(key); err != nil { // Ruling 12
		t.Fatal(err)
	}
	args := []string{"register", "-server", base, "-identity", "pat", "-credential", key}
	code, out, errs := run(args, environ)
	if code != 0 || out != "registered pat at "+base+" (created)\n" || errs != "" {
		t.Fatalf("first run: code %d, stdout %q, stderr %q", code, out, errs)
	}
	code, out, errs = run(args, environ)
	if code != 0 || out != "registered pat at "+base+" (existing key)\n" || errs != "" {
		t.Fatalf("second run: code %d, stdout %q, stderr %q", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(configDir, "credentials")); !os.IsNotExist(err) {
		t.Errorf("credentials directory exists (err %v): -credential writes no credential or pending file", err)
	}
}
