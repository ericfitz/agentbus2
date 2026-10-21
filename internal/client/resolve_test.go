package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// env builds temp config and data directories and an environ naming them.
type env struct {
	home, config, data string
	environ            []string
}

func newEnv(t *testing.T, extra ...string) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{home: filepath.Join(root, "home"), config: filepath.Join(root, "config"), data: filepath.Join(root, "data")}
	for _, d := range []string{e.home, e.config, e.data} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e.environ = append([]string{"HOME=" + e.home, "AGENTBUS2_CONFIG_DIR=" + e.config, "AGENTBUS2_DATA_DIR=" + e.data}, extra...)
	return e
}

func (e *env) save(t *testing.T, ps ...profile.Profile) {
	t.Helper()
	c := profile.Config{Path: filepath.Join(e.config, "config.json"), Profiles: map[string]profile.Profile{}}
	for _, p := range ps {
		c.Profiles[p.Name] = p
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
}

func (e *env) credential(t *testing.T, profileName, identity string) {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.config, profileName)}).Save(identity, k); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOrder(t *testing.T) {
	e := newEnv(t)
	e.save(t,
		profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "pat"},
		profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"},
		profile.Profile{Name: "repo", Mode: profile.ModeServer, Server: "http://127.0.0.1:2"},
		profile.Profile{Name: "envp", Mode: profile.ModeServer, Server: "http://127.0.0.1:3"},
	)
	e.credential(t, "lab", "kim")
	e.credential(t, "repo", "kim")
	e.credential(t, "envp", "kim")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".local", "agentbus2.json"), []byte(`{"profile":"repo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		o            Options
		profile, src string
		identity     string
		idSrc        string
	}{
		{"flag over env over repo", Options{Profile: "lab", Cwd: repo, Environ: append(append([]string{}, e.environ...), "AGENTBUS2_PROFILE=envp")}, "lab", "flag", "kim", "credential"},
		{"env over repo", Options{Cwd: repo, Environ: append(append([]string{}, e.environ...), "AGENTBUS2_PROFILE=envp")}, "envp", "env", "kim", "credential"},
		{"repo over default", Options{Cwd: repo, Environ: e.environ}, "repo", "repo", "kim", "credential"},
		{"default", Options{Cwd: t.TempDir(), Environ: e.environ}, "default", "default", "pat", "profile"},
		{"identity flag over env over profile", Options{Cwd: t.TempDir(), Identity: "sam", Environ: append(append([]string{}, e.environ...), "AGENTBUS2_IDENTITY=lee")}, "default", "default", "sam", "flag"},
		{"identity env over profile", Options{Cwd: t.TempDir(), Environ: append(append([]string{}, e.environ...), "AGENTBUS2_IDENTITY=lee")}, "default", "default", "lee", "env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Resolve(c.o)
			if err != nil {
				t.Fatal(err)
			}
			if r.Profile.Name != c.profile || r.ProfileSource != c.src || r.Identity != c.identity || r.IdentitySource != c.idSrc {
				t.Fatalf("got profile %q (%s) identity %q (%s), want %q (%s) %q (%s)",
					r.Profile.Name, r.ProfileSource, r.Identity, r.IdentitySource, c.profile, c.src, c.identity, c.idSrc)
			}
			if r.ConfigDir != e.config || r.DataDir != e.data {
				t.Fatalf("dirs %q %q", r.ConfigDir, r.DataDir)
			}
		})
	}

	t.Run("local default without identity", func(t *testing.T) {
		e2 := newEnv(t)
		_, err := Resolve(Options{Cwd: t.TempDir(), Environ: e2.environ})
		if err == nil || !strings.Contains(err.Error(), "identity is required") {
			t.Fatalf("err %v, want the three-way identity error", err)
		}
		var re *ResolveError
		if !errors.As(err, &re) {
			t.Fatalf("err %T is not a *ResolveError", err)
		}
	})

	t.Run("two credentials and no setting", func(t *testing.T) {
		e2 := newEnv(t)
		e2.save(t, profile.Profile{Name: "default", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
		e2.credential(t, "default", "kim")
		e2.credential(t, "default", "lee")
		if _, err := Resolve(Options{Cwd: t.TempDir(), Environ: e2.environ}); err == nil || !strings.Contains(err.Error(), "identity is required") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("undefined profile", func(t *testing.T) {
		e2 := newEnv(t)
		_, err := Resolve(Options{Profile: "nope", Cwd: t.TempDir(), Environ: e2.environ})
		if err == nil || !strings.Contains(err.Error(), `profile "nope" is not defined`) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestResolveRepoFileError(t *testing.T) {
	e := newEnv(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".local", "agentbus2.json"), []byte(`{"profile":"lab","credential_process":["x"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Options{Cwd: repo, Environ: e.environ})
	if err == nil || !strings.Contains(err.Error(), "credential_process") {
		t.Fatalf("err %v, want an error naming credential_process", err)
	}
	var re *ResolveError
	if !errors.As(err, &re) {
		t.Fatalf("err %T is not a *ResolveError", err)
	}
}

func TestResolveReadsOnlyGivenEnviron(t *testing.T) {
	// The process environment names a directory; Resolve must ignore it.
	t.Setenv("AGENTBUS2_PROFILE", "from-process-env")
	t.Setenv("AGENTBUS2_IDENTITY", "from-process-env")
	e := newEnv(t)
	e.save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "pat"})
	r, err := Resolve(Options{Cwd: t.TempDir(), Environ: e.environ})
	if err != nil || r.Profile.Name != "default" || r.Identity != "pat" {
		t.Fatalf("%+v, %v", r, err)
	}
}
