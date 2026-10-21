package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/profile"
)

func TestInitRepoCreatesProfile(t *testing.T) {
	e := newTestEnv(t)
	e.writeProfile(t, profile.Profile{Name: "default", Mode: profile.ModeServer, Server: "https://h:8443"})
	repo := filepath.Join(t.TempDir(), "lab-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	o := InitOptions{Home: e.Home, Cwd: sub, Environ: e.Environ, OS: "linux"}
	if err := Init(o, &out); err != nil {
		t.Fatalf("Init: %v\n%s", err, out.String())
	}
	body, err := os.ReadFile(filepath.Join(repo, ".local", "agentbus2.json"))
	if err != nil || strings.Join(strings.Fields(string(body)), "") != `{"profile":"lab-repo"}` {
		t.Fatalf("repo file = %q, %v", body, err)
	}
	cfg, err := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := profile.Profile{Name: "lab-repo", Mode: profile.ModeServer, Server: "https://h:8443", Identity: "lab-repo"}
	if got := cfg.Profiles["lab-repo"]; got.Mode != want.Mode || got.Server != want.Server || got.Identity != want.Identity {
		t.Fatalf("profile = %+v, want %+v", got, want)
	}
	if d := cfg.Profiles["default"]; d.Server != "https://h:8443" {
		t.Fatalf("default changed: %+v", d)
	}
	if gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); string(gi) != ".local/\n" {
		t.Fatalf(".gitignore = %q", gi)
	}
	if !strings.Contains(out.String(), "agentbus2 register") {
		t.Fatalf("output must say to run agentbus2 register:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(e.ConfigDir, "config.json.bak")); err != nil {
		t.Fatalf("editing an existing config.json must leave a backup: %v", err)
	}

	// A second run changes nothing, and does not ask for a registration once the
	// credential exists.
	before, _ := os.ReadFile(filepath.Join(e.ConfigDir, "config.json"))
	out.Reset()
	if err := Init(o, &out); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(e.ConfigDir, "config.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("second run rewrote config.json")
	}
	if gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); string(gi) != ".local/\n" {
		t.Fatalf("second run changed .gitignore: %q", gi)
	}
	if !strings.Contains(out.String(), "left unchanged") {
		t.Fatalf("an existing profile is left unchanged:\n%s", out.String())
	}

	// An existing profile is not modified even when -identity disagrees.
	out.Reset()
	o2 := o
	o2.Identity = "someone-else"
	if err := Init(o2, &out); err != nil {
		t.Fatal(err)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if cfg.Profiles["lab-repo"].Identity != "lab-repo" {
		t.Fatalf("identity changed: %+v", cfg.Profiles["lab-repo"])
	}

	// -profile selects another profile and rewrites the repo file.
	out.Reset()
	o3 := o
	o3.Profile = "x"
	if err := Init(o3, &out); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(filepath.Join(repo, ".local", "agentbus2.json"))
	if !strings.Contains(string(body), `"x"`) {
		t.Fatalf("repo file = %q", body)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if p, ok := cfg.Profiles["x"]; !ok || p.Identity != "lab-repo" || p.Server != "https://h:8443" {
		t.Fatalf("profile x = %+v", p)
	}
}

func TestInitRepoNeedsAValidIdentity(t *testing.T) {
	e := newTestEnv(t)
	// A repository name that is a valid directory name but not a valid
	// identity (NameRule allows 128 bytes).
	repo := filepath.Join(t.TempDir(), strings.Repeat("a", 129))
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Skipf("cannot create a 129-byte directory name here: %v", err)
	}
	var out bytes.Buffer
	o := InitOptions{Home: e.Home, Cwd: repo, Environ: e.Environ, OS: "linux", Profile: "p1"}
	err := Init(o, &out)
	if err == nil || !strings.Contains(err.Error(), "-identity") {
		t.Fatalf("err = %v, want one naming -identity", err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, ".local")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused init wrote .local: %v", statErr)
	}
	cfg, _ := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if _, ok := cfg.Profiles["p1"]; ok {
		t.Fatal("a refused init added the profile")
	}
	o.Identity = "good"
	if err := Init(o, &out); err != nil {
		t.Fatalf("with -identity: %v", err)
	}
	cfg, _ = profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if cfg.Profiles["p1"].Identity != "good" {
		t.Fatalf("profile = %+v", cfg.Profiles["p1"])
	}
	// A bad -identity is refused too.
	o.Profile, o.Identity = "p2", "a/b"
	if err := Init(o, &out); err == nil || !strings.Contains(err.Error(), "-identity") {
		t.Fatalf("err = %v", err)
	}
	// A repository name that cannot be a profile name needs -profile.
	repo2 := filepath.Join(t.TempDir(), strings.Repeat("b", 129))
	if err := os.MkdirAll(filepath.Join(repo2, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.Cwd, o.Profile, o.Identity = repo2, "", "good"
	if err := Init(o, &out); err == nil || !strings.Contains(err.Error(), "-profile") {
		t.Fatalf("err = %v, want one naming -profile", err)
	}
}

func TestInitRepoLocalDefault(t *testing.T) {
	e := newTestEnv(t)
	if err := os.Remove(filepath.Join(e.ConfigDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "lab-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Init(InitOptions{Home: e.Home, Cwd: repo, Environ: e.Environ, OS: "linux"}, &out); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg, err := profile.Load(filepath.Join(e.ConfigDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Profiles["lab-repo"]; p.Mode != profile.ModeLocal || p.Identity != "lab-repo" || p.Server != "" {
		t.Fatalf("profile = %+v", p)
	}
	if gi, _ := os.ReadFile(filepath.Join(repo, ".gitignore")); string(gi) != "node_modules\n.local/\n" {
		t.Fatalf(".gitignore = %q", gi)
	}
	if strings.Contains(out.String(), "agentbus2 register") {
		t.Fatalf("a local profile needs no registration:\n%s", out.String())
	}
	// -global inside a repository runs the machine step instead.
	out.Reset()
	if err := Init(InitOptions{Global: true, Harness: "grok", DryRun: true, Home: e.Home, Cwd: repo, Environ: e.Environ, OS: "linux"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Grok Build:") {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestInitRepoDryRunWritesNothing(t *testing.T) {
	e := newTestEnv(t)
	repo := filepath.Join(t.TempDir(), "lab-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(e.ConfigDir, "config.json"))
	var out bytes.Buffer
	if err := Init(InitOptions{DryRun: true, Home: e.Home, Cwd: repo, Environ: e.Environ, OS: "linux"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".local")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote .local: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote .gitignore: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(e.ConfigDir, "config.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("dry run changed config.json")
	}
	if !strings.Contains(out.String(), "would add profile lab-repo") {
		t.Fatalf("output:\n%s", out.String())
	}
}
