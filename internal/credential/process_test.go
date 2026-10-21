//go:build !windows

package credential

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// procArgv returns the argv of the fake process in mode and sets its environment.
func procArgv(t *testing.T, mode string) []string {
	t.Helper()
	t.Setenv("CREDPROC_MODE", mode)
	return []string{"bash", filepath.Join("testdata", "credproc.sh")}
}

func in() ProcessInput {
	return ProcessInput{Identity: "pat", TokenEndpoint: "http://h/v1/auth/token", Audience: "http://h/v1/auth/token", JTI: "AAAAAAAAAAAAAAAAAAAAAA"}
}

func TestProcessInputShape(t *testing.T) {
	argv := procArgv(t, "token")
	cap := filepath.Join(t.TempDir(), "stdin.json")
	t.Setenv("CREDPROC_CAPTURE", cap)
	t.Setenv("CREDPROC_TOKEN", "tok")
	t.Setenv("CREDPROC_EXPIRES", "2099-01-01T00:00:00Z")
	if _, err := RunProcess(context.Background(), argv, in()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cap)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 5 || m["version"] != float64(1) || m["identity"] != "pat" || m["token_endpoint"] != "http://h/v1/auth/token" || m["audience"] != "http://h/v1/auth/token" {
		t.Errorf("stdin %s", b)
	}
	if jti, _ := m["jti"].(string); len(jti) != 22 {
		t.Errorf("jti %q", jti)
	}
}

func TestProcessEachResult(t *testing.T) {
	k, _ := keys.Generate(keys.EdDSA)
	pem, _ := keys.EncodePrivatePEM(k)
	t.Setenv("CREDPROC_PEM", string(pem))
	t.Setenv("CREDPROC_ASSERTION", "a.b.c")
	t.Setenv("CREDPROC_TOKEN", "tok")
	t.Setenv("CREDPROC_EXPIRES", "2099-01-01T00:00:00Z")

	o, err := RunProcess(context.Background(), procArgv(t, "pem"), in())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := keys.ParsePrivatePEM([]byte(o.PrivateKeyPEM)); err != nil {
		t.Errorf("pem does not parse: %v", err)
	}
	o, err = RunProcess(context.Background(), procArgv(t, "assertion"), in())
	if err != nil || o.ClientAssertion != "a.b.c" {
		t.Errorf("assertion: %+v %v", o, err)
	}
	o, err = RunProcess(context.Background(), procArgv(t, "token"), in())
	if err != nil || o.AccessToken != "tok" || o.ExpiresAt != "2099-01-01T00:00:00Z" {
		t.Errorf("token: %+v %v", o, err)
	}
}

func TestProcessRejects(t *testing.T) {
	t.Setenv("CREDPROC_TOKEN", "tok")
	t.Setenv("CREDPROC_EXPIRES", "tomorrow")
	for _, mode := range []string{"two", "none", "garbage", "exit1", "version2", "unknown", "trailing", "big", "token-no-expiry", "token", "nosuchmode"} {
		t.Run(mode, func(t *testing.T) {
			argv := procArgv(t, mode)
			_, err := RunProcess(context.Background(), argv, in())
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), `"bash"`) {
				t.Errorf("error does not name the program: %v", err)
			}
			if mode == "exit1" && !strings.Contains(err.Error(), "vault unreachable") {
				t.Errorf("stderr not quoted: %v", err)
			}
			if mode == "two" && !strings.Contains(err.Error(), "exactly one of private_key_pem, client_assertion or access_token") {
				t.Errorf("message: %v", err)
			}
		})
	}
	if _, err := RunProcess(context.Background(), []string{"/nonexistent/credproc"}, in()); err == nil || !strings.Contains(err.Error(), "/nonexistent/credproc") {
		t.Errorf("missing executable: %v", err)
	}
	if _, err := RunProcess(context.Background(), nil, in()); err == nil {
		t.Error("empty argv accepted")
	}
}

func TestProcessExpiresAtOnlyWithToken(t *testing.T) {
	// A hand-made process: expires_at beside a client assertion is refused.
	_, err := RunProcess(context.Background(), []string{"sh", "-c", `echo '{"version":1,"client_assertion":"a","expires_at":"2099-01-01T00:00:00Z"}'`}, in())
	if err == nil {
		t.Error("expires_at beside a client assertion accepted")
	}
}

func TestProcessTimeout(t *testing.T) {
	argv := procArgv(t, "hang")
	pidfile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("CREDPROC_PIDFILE", pidfile)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := RunProcess(ctx, argv, in())
	if err == nil {
		t.Fatal("hang accepted")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}
	b, _ := os.ReadFile(pidfile)
	pid, perr := strconv.Atoi(string(b))
	if perr != nil {
		t.Fatalf("pid file %q", b)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Errorf("process %d still exists", pid)
	}
}
