package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

func TestLoginExchangesAndCaches(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)

	sctx, cancel := context.WithCancel(ctx)
	srvOut, srvErr := &syncBuf{}, &syncBuf{}
	done := make(chan int, 1)
	go func() {
		done <- cmd.Run(sctx, []string{"-listen", "127.0.0.1:0", "-sqlite", filepath.Join(t.TempDir(), "srv.sqlite")}, nil, srvOut, srvErr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("server did not stop")
		}
	})
	re := regexp.MustCompile(`listening on (\S+) `)
	base := ""
	for deadline := time.Now().Add(10 * time.Second); base == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := re.FindStringSubmatch(srvOut.String()); m != nil {
			base = "http://" + m[1]
		}
	}
	if base == "" {
		t.Fatalf("no listening line: %s %s", srvOut.String(), srvErr.String())
	}

	out, errb := &syncBuf{}, &syncBuf{}
	if code := RunRegister(ctx, []string{"-profile", "lab", "-server", base, "-identity", "pat"}, e.Environ, nil, out, errb); code != 0 {
		t.Fatalf("register: %d %s", code, errb.String())
	}
	cache := filepath.Join(e.DataDir, "tokens", "lab", "pat.json")
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatalf("register must not mint a token: %v", err)
	}

	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "lab"}, e.Environ, nil, out, errb); code != 0 {
		t.Fatalf("login: %d %s", code, errb.String())
	}
	line := regexp.MustCompile(`^logged in as pat at (\S+) \(token expires (\S+)\)\n$`).FindStringSubmatch(out.String())
	if line == nil {
		t.Fatalf("login output %q", out.String())
	}
	if line[1] == "" {
		t.Fatalf("the line names no issuer: %q", out.String())
	}
	if _, err := time.Parse(time.RFC3339, line[2]); err != nil {
		t.Fatalf("expiry %q: %v", line[2], err)
	}
	fi, err := os.Stat(cache)
	if err != nil {
		t.Fatalf("no token cache: %v", err)
	}

	// A second login exchanges again (it proves the credential) and still
	// succeeds; an assertion's jti is single-use, so this also shows each
	// exchange signs a fresh one.
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "lab"}, e.Environ, nil, out, errb); code != 0 {
		t.Fatalf("second login: %d %s", code, errb.String())
	}
	if fi2, _ := os.Stat(cache); fi2 == nil || fi2.Size() == 0 || fi2.Mode().Perm() != fi.Mode().Perm() {
		t.Fatalf("cache after a second login: %v", fi2)
	}

	// A local profile: exit 2, with the mode message, even with no identity.
	e.writeProfile(t, profile.Profile{Name: "loc", Mode: profile.ModeLocal})
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "loc"}, e.Environ, nil, out, errb); code != 2 || !strings.Contains(errb.String(), "login applies to server profiles only") {
		t.Fatalf("local: %d %q", code, errb.String())
	}
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "default"}, e.Environ, nil, out, errb); code != 2 {
		t.Fatalf("default: %d %q", code, errb.String())
	}

	// A server profile with no credential: exit 2 naming register.
	e.writeProfile(t, profile.Profile{Name: "bare", Mode: profile.ModeServer, Server: base, Identity: "kim"})
	out, errb = &syncBuf{}, &syncBuf{}
	if code := RunLogin(ctx, []string{"-profile", "bare"}, e.Environ, nil, out, errb); code != 2 || !strings.Contains(errb.String(), "agentbus2 register") {
		t.Fatalf("no credential: %d %q", code, errb.String())
	}
	if code := RunLogin(ctx, []string{"-profile", "nosuch"}, e.Environ, nil, out, errb); code != 2 {
		t.Fatalf("undefined profile: %d", code)
	}
	if code := RunLogin(ctx, []string{"extra"}, e.Environ, nil, out, errb); code != 2 {
		t.Fatalf("extra argument: %d", code)
	}
}
