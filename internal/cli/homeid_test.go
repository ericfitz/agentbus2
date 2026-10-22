package cli

import (
	"bytes"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
)

func setLogin(t *testing.T, name string) {
	t.Helper()
	prev := currentUser
	currentUser = func() (*user.User, error) { return &user.User{Username: name}, nil }
	t.Cleanup(func() { currentUser = prev })
}

// initIdentity runs Init in repo (with home as the home directory) and returns
// the identity of the profile it wrote.
func initIdentity(t *testing.T, e *testEnv, home, repo, flagIdentity string) string {
	t.Helper()
	var out bytes.Buffer
	o := InitOptions{Home: home, Cwd: repo, Environ: e.Environ, OS: "linux", Identity: flagIdentity}
	if err := Init(o, &out); err != nil {
		t.Fatalf("Init: %v\n%s", err, out.String())
	}
	cfg, err := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Profiles[filepath.Base(repo)].Identity
}

func mkRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInitHomeDirectoryIdentity(t *testing.T) {
	setLogin(t, "efitz")
	t.Run("repository at HOME", func(t *testing.T) {
		e := newTestEnv(t)
		mkRepo(t, e.Home)
		if got := initIdentity(t, e, e.Home, e.Home, ""); got != "efitz-home" {
			t.Fatalf("identity = %q, want efitz-home", got)
		}
	})
	t.Run("symlinked HOME", func(t *testing.T) {
		e := newTestEnv(t)
		mkRepo(t, e.Home)
		link := filepath.Join(t.TempDir(), "homelink")
		if err := os.Symlink(e.Home, link); err != nil {
			t.Skip(err)
		}
		if got := initIdentity(t, e, link, e.Home, ""); got != "efitz-home" {
			t.Fatalf("identity = %q, want efitz-home", got)
		}
	})
	t.Run("explicit identity kept", func(t *testing.T) {
		e := newTestEnv(t)
		mkRepo(t, e.Home)
		if got := initIdentity(t, e, e.Home, e.Home, "efitz"); got != "efitz" {
			t.Fatalf("identity = %q, want efitz", got)
		}
	})
	t.Run("other directory named like the user", func(t *testing.T) {
		e := newTestEnv(t)
		repo := filepath.Join(t.TempDir(), "efitz")
		mkRepo(t, repo)
		if got := initIdentity(t, e, e.Home, repo, ""); got != "efitz" {
			t.Fatalf("identity = %q, want efitz", got)
		}
	})
	t.Run("unknown login", func(t *testing.T) {
		setLogin(t, "")
		e := newTestEnv(t)
		repo := filepath.Join(t.TempDir(), "box")
		mkRepo(t, repo)
		if got := initIdentity(t, e, repo, repo, ""); got != "box" {
			t.Fatalf("identity = %q, want box", got)
		}
	})
}

func TestHomeDerivedIdentityHomeError(t *testing.T) {
	_, ok := homeDerivedIdentity(t.TempDir(), func() (string, error) { return "", errors.New("no home") }, func() string { return "u" })
	if ok {
		t.Fatal("a home lookup error must not rewrite the identity")
	}
}

func TestMCPRefusesLoginName(t *testing.T) {
	setLogin(t, "efitz")
	e := newTestEnv(t)
	var stdout, stderr bytes.Buffer
	code := runMCP(t.Context(), []string{"-identity", "efitz"}, e.Environ, strings.NewReader(""), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout must stay empty, got %q", stdout.String())
	}
	for _, w := range []string{"efitz", "-identity", `profile "default"`, filepath.Join(e.ConfigDir, "config.json")} {
		if !strings.Contains(stderr.String(), w) {
			t.Fatalf("stderr %q lacks %q", stderr.String(), w)
		}
	}
	// The same refusal through the environment and the profile's identity.
	stderr.Reset()
	if code := runMCP(t.Context(), nil, append(append([]string{}, e.Environ...), "AGENTBUS2_IDENTITY=efitz"), strings.NewReader(""), &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("env identity: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "efitz"})
	stderr.Reset()
	if code := runMCP(t.Context(), nil, e.Environ, strings.NewReader(""), &stdout, &stderr); code != 1 || stdout.Len() != 0 {
		t.Fatalf("profile identity: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestMCPGuardAllows(t *testing.T) {
	e := newTestEnv(t)
	for _, tc := range []struct{ login, identity string }{
		{"efitz", "efitz-home"}, {"efitz", "pat"}, {"", "efitz"},
	} {
		setLogin(t, tc.login)
		if err := mcpIdentityGuard(client.Options{Environ: e.Environ, Cwd: e.Home, Identity: tc.identity}); err != nil {
			t.Errorf("login %q identity %q: %v", tc.login, tc.identity, err)
		}
	}
}

func TestInitIgnoredIdentityNote(t *testing.T) {
	setLogin(t, "efitz")
	e := newTestEnv(t)
	repo := filepath.Join(t.TempDir(), "proj")
	mkRepo(t, repo)
	run := func(id string) string {
		var out bytes.Buffer
		if err := Init(InitOptions{Home: e.Home, Cwd: repo, Environ: e.Environ, OS: "linux", Identity: id}, &out); err != nil {
			t.Fatalf("Init: %v\n%s", err, out.String())
		}
		return out.String()
	}
	if out := run(""); strings.Contains(out, "ignored") {
		t.Fatalf("first run must not note an ignored identity:\n%s", out)
	}
	out := run("other")
	for _, w := range []string{"-identity other ignored", `keeps its identity "proj"`, filepath.Join(e.ConfigDir, "config.json")} {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
	if out := run(""); strings.Contains(out, "ignored") {
		t.Fatalf("no -identity, no note:\n%s", out)
	}
}
