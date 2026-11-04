package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentmesh/internal/auth/keys"
	"github.com/ericfitz/agentmesh/internal/credential"
	"github.com/ericfitz/agentmesh/internal/profile"
	"github.com/ericfitz/agentmesh/internal/testkit"
)

// saveCredential writes a generated agent key for the profile and identity.
func saveCredential(t *testing.T, e *testkit.ProfileEnv, profileName, identity string) {
	t.Helper()
	k, err := keys.Generate(keys.EdDSA)
	if err != nil {
		t.Fatal(err)
	}
	if err := (credential.Store{Dir: profile.CredentialDir(e.Config, profileName)}).Save(identity, k); err != nil {
		t.Fatal(err)
	}
}

func TestResolveOrder(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	e.Save(t,
		profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "pat"},
		profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"},
		profile.Profile{Name: "repo", Mode: profile.ModeServer, Server: "http://127.0.0.1:2"},
		profile.Profile{Name: "envp", Mode: profile.ModeServer, Server: "http://127.0.0.1:3"},
	)
	saveCredential(t, e, "lab", "kim")
	saveCredential(t, e, "repo", "kim")
	saveCredential(t, e, "envp", "kim")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".local", "agentmesh.json"), []byte(`{"profile":"repo"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		o            Options
		profile, src string
		identity     string
		idSrc        string
	}{
		{"flag over env over repo", Options{Profile: "lab", Cwd: repo, Environ: append(append([]string{}, e.Environ...), "AGENTMESH_PROFILE=envp")}, "lab", "flag", "kim", "credential"},
		{"env over repo", Options{Cwd: repo, Environ: append(append([]string{}, e.Environ...), "AGENTMESH_PROFILE=envp")}, "envp", "env", "kim", "credential"},
		{"repo over default", Options{Cwd: repo, Environ: e.Environ}, "repo", "repo", "kim", "credential"},
		{"default", Options{Cwd: t.TempDir(), Environ: e.Environ}, "default", "default", "pat", "profile"},
		{"identity flag over env over profile", Options{Cwd: t.TempDir(), Identity: "sam", Environ: append(append([]string{}, e.Environ...), "AGENTMESH_IDENTITY=lee")}, "default", "default", "sam", "flag"},
		{"identity env over profile", Options{Cwd: t.TempDir(), Environ: append(append([]string{}, e.Environ...), "AGENTMESH_IDENTITY=lee")}, "default", "default", "lee", "env"},
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
			if r.ConfigDir != e.Config || r.DataDir != e.Data {
				t.Fatalf("dirs %q %q", r.ConfigDir, r.DataDir)
			}
		})
	}

	t.Run("local default without identity", func(t *testing.T) {
		e2 := testkit.NewProfileEnv(t)
		_, err := Resolve(Options{Cwd: t.TempDir(), Environ: e2.Environ})
		if err == nil || !strings.Contains(err.Error(), "identity is required") {
			t.Fatalf("err %v, want the three-way identity error", err)
		}
		var re *ResolveError
		if !errors.As(err, &re) {
			t.Fatalf("err %T is not a *ResolveError", err)
		}
		if !errors.Is(err, profile.ErrIdentityRequired) {
			t.Fatalf("err %v does not wrap profile.ErrIdentityRequired", err)
		}
	})

	t.Run("fallback identity", func(t *testing.T) {
		e2 := testkit.NewProfileEnv(t)
		r, err := Resolve(Options{Cwd: t.TempDir(), Environ: e2.Environ, FallbackIdentity: "efitz"})
		if err != nil || r.Identity != "efitz" || r.IdentitySource != "os-user" {
			t.Fatalf("got %q (%s), %v", r.Identity, r.IdentitySource, err)
		}
	})

	t.Run("two credentials and no setting", func(t *testing.T) {
		e2 := testkit.NewProfileEnv(t)
		e2.Save(t, profile.Profile{Name: "default", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
		saveCredential(t, e2, "default", "kim")
		saveCredential(t, e2, "default", "lee")
		if _, err := Resolve(Options{Cwd: t.TempDir(), Environ: e2.Environ}); err == nil || !strings.Contains(err.Error(), "identity is required") {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("undefined profile", func(t *testing.T) {
		e2 := testkit.NewProfileEnv(t)
		_, err := Resolve(Options{Profile: "nope", Cwd: t.TempDir(), Environ: e2.Environ})
		if err == nil || !strings.Contains(err.Error(), `profile "nope" is not defined`) {
			t.Fatalf("err %v", err)
		}
	})
}

func TestResolveRepoFileError(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".local", "agentmesh.json"), []byte(`{"profile":"lab","credential_process":["x"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(Options{Cwd: repo, Environ: e.Environ})
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
	t.Setenv("AGENTMESH_PROFILE", "from-process-env")
	t.Setenv("AGENTMESH_IDENTITY", "from-process-env")
	e := testkit.NewProfileEnv(t)
	e.Save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "pat"})
	r, err := Resolve(Options{Cwd: t.TempDir(), Environ: e.Environ})
	if err != nil || r.Profile.Name != "default" || r.Identity != "pat" {
		t.Fatalf("%+v, %v", r, err)
	}
}

// TestResolveHumanIdentity: with HumanIdentity the profile's identity counts
// only when the profile was not chosen by the repository file (TUI decision
// 11), so the human never shares the agent's identity.
func TestResolveHumanIdentity(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	e.Save(t,
		profile.Profile{Name: "default", Mode: profile.ModeLocal, Identity: "eric"},
		profile.Profile{Name: "repo", Mode: profile.ModeLocal, Identity: "agentmesh"},
		profile.Profile{Name: "homerepo", Mode: profile.ModeLocal, Identity: "efitz-home"},
		profile.Profile{Name: "lab", Mode: profile.ModeLocal, Identity: "eric"},
	)
	repoWith := func(name string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".local"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".local", "agentmesh.json"), []byte(`{"profile":"`+name+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cases := []struct {
		name         string
		o            Options
		identity, id string
	}{
		{"repo profile gives the login name", Options{Cwd: repoWith("repo")}, "efitz", "os-user"},
		{"home repo profile gives the login name", Options{Cwd: repoWith("homerepo")}, "efitz", "os-user"},
		{"identity flag wins in a repo", Options{Cwd: repoWith("repo"), Identity: "sam"}, "sam", "flag"},
		{"profile flag keeps its identity", Options{Cwd: repoWith("repo"), Profile: "lab"}, "eric", "profile"},
		{"default fallback keeps its identity", Options{Cwd: t.TempDir()}, "eric", "profile"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.o.Environ, c.o.HumanIdentity, c.o.FallbackIdentity = e.Environ, true, "efitz"
			r, err := Resolve(c.o)
			if err != nil || r.Identity != c.identity || r.IdentitySource != c.id {
				t.Fatalf("got %q (%s), %v; want %q (%s)", r.Identity, r.IdentitySource, err, c.identity, c.id)
			}
		})
	}
	t.Run("env profile keeps its identity", func(t *testing.T) {
		r, err := Resolve(Options{Cwd: repoWith("repo"), Environ: append(append([]string{}, e.Environ...), "AGENTMESH_PROFILE=lab"), HumanIdentity: true, FallbackIdentity: "efitz"})
		if err != nil || r.Identity != "eric" {
			t.Fatalf("got %q, %v", r.Identity, err)
		}
	})
	t.Run("env identity wins in a repo", func(t *testing.T) {
		r, err := Resolve(Options{Cwd: repoWith("repo"), Environ: append(append([]string{}, e.Environ...), "AGENTMESH_IDENTITY=lee"), HumanIdentity: true, FallbackIdentity: "efitz"})
		if err != nil || r.Identity != "lee" || r.IdentitySource != "env" {
			t.Fatalf("got %q (%s), %v", r.Identity, r.IdentitySource, err)
		}
	})
	t.Run("other commands keep the repo identity", func(t *testing.T) {
		r, err := Resolve(Options{Cwd: repoWith("repo"), Environ: e.Environ})
		if err != nil || r.Identity != "agentmesh" || r.IdentitySource != "profile" {
			t.Fatalf("got %q (%s), %v", r.Identity, r.IdentitySource, err)
		}
	})
}

// TestResolveHumanIdentityServerRepo: a repository-selected server profile
// holds the agent's only credential; the TUI must not take it, other commands
// still do.
func TestResolveHumanIdentityServerRepo(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	e.Save(t, profile.Profile{Name: "repo", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	saveCredential(t, e, "repo", "agentmesh")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".local"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".local", "agentmesh.json"), []byte(`{"profile":"repo"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	human := Options{Cwd: dir, Environ: e.Environ, HumanIdentity: true, FallbackIdentity: "efitz"}
	if _, err := Resolve(human); err == nil || !strings.Contains(err.Error(), "identity is required") {
		t.Fatalf("err %v, want identity is required", err)
	}
	r, err := Resolve(Options{Cwd: dir, Environ: e.Environ})
	if err != nil || r.Identity != "agentmesh" || r.IdentitySource != "credential" {
		t.Fatalf("other commands: %q (%s), %v", r.Identity, r.IdentitySource, err)
	}
	saveCredential(t, e, "repo", "efitz")
	r, err = Resolve(human)
	if err != nil || r.Identity != "efitz" || r.IdentitySource != "os-user" {
		t.Fatalf("with an efitz credential: %q (%s), %v", r.Identity, r.IdentitySource, err)
	}
}

// open note: mcp-only-client 3
func TestResolveServerFlag(t *testing.T) {
	lab := profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://h:1"}
	loc := profile.Profile{Name: "loc", Mode: profile.ModeLocal}
	cases := []struct {
		name      string
		saved     []profile.Profile
		profile   string
		server    string
		wantName  string
		wantSrv   string
		wantInErr string
	}{
		{"undefined name", nil, "lab", "http://h:1/", "lab", "http://h:1", ""},
		{"undefined default", nil, "", "http://h:1/", "default", "http://h:1", ""},
		{"defined, same server", []profile.Profile{lab}, "lab", "http://h:1/", "lab", "http://h:1", ""},
		{"defined, other server", []profile.Profile{lab}, "lab", "http://h:2", "", "", "disagrees"},
		{"defined local", []profile.Profile{loc}, "loc", "http://h:1", "", "", "-server applies to server profiles"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testkit.NewProfileEnv(t)
			if c.saved != nil {
				e.Save(t, c.saved...)
			}
			r, err := Resolve(Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: c.profile, Identity: "pat", Server: c.server})
			if c.wantInErr != "" {
				var re *ResolveError
				if !errors.As(err, &re) || !strings.Contains(err.Error(), c.wantInErr) {
					t.Fatalf("err %v (%T), want a ResolveError containing %q", err, err, c.wantInErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Profile.Name != c.wantName || r.Profile.Mode != profile.ModeServer || r.Profile.Server != c.wantSrv {
				t.Fatalf("profile %+v", r.Profile)
			}
			if c.saved == nil {
				if _, err := os.Stat(filepath.Join(e.Config, "config.json")); !os.IsNotExist(err) {
					t.Fatalf("config.json exists after a -server resolve: %v", err)
				}
			}
		})
	}
}

// open note: mcp-only-client 4
func TestResolveCredentialFlag(t *testing.T) {
	proc := profile.Profile{Name: "proc", Mode: profile.ModeServer, Server: "http://h:1", CredentialProcess: []string{"x"}}
	loc := profile.Profile{Name: "loc", Mode: profile.ModeLocal}
	oidc := profile.Profile{Name: "oidc", Mode: profile.ModeServer, Server: "http://h:1", OIDC: &profile.OIDC{Issuer: "https://i", ClientID: "c", RedirectPort: 8123, Scopes: []string{"openid"}}}
	cases := []struct {
		name  string
		p     profile.Profile
		human bool
		want  string
	}{
		{"local", loc, false, `-credential applies to server profiles; profile "loc" is local`},
		{"credential_process", proc, false, `profile "proc" sets credential_process; -credential cannot be combined with it`},
		{"human sign-in", oidc, true, "-credential does not apply to a human sign-in"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := testkit.NewProfileEnv(t)
			e.Save(t, c.p)
			_, err := Resolve(Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: c.p.Name, Identity: "pat", Credential: "/k.pem", HumanSignIn: c.human})
			var re *ResolveError
			if !errors.As(err, &re) || err.Error() != c.want {
				t.Fatalf("err %v, want a ResolveError %q", err, c.want)
			}
			// Only the credential_process case matches the sentinel.
			if got := errors.Is(err, ErrCredentialProcessConflict); got != (c.name == "credential_process") {
				t.Fatalf("errors.Is(err, ErrCredentialProcessConflict) = %v for %s", got, c.name)
			}
		})
	}
	t.Run("server profile resolves without a credential directory read", func(t *testing.T) {
		e := testkit.NewProfileEnv(t)
		r, err := Resolve(Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: "lab", Server: "http://h:1", Identity: "pat", Credential: "/k.pem"})
		if err != nil || r.Identity != "pat" {
			t.Fatalf("%+v, %v", r, err)
		}
	})
}

func TestResolveCarriesHooks(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	if err := os.WriteFile(filepath.Join(e.Config, "config.json"), []byte(`{"profiles":{},"hooks":{"status_stale_minutes":12}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Resolve(Options{Environ: e.Environ, Cwd: t.TempDir(), FallbackIdentity: "pat"})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Hooks.StaleMinutes(); got != 12 {
		t.Fatalf("StaleMinutes = %d, want 12", got)
	}
	e2 := testkit.NewProfileEnv(t)
	r, err = Resolve(Options{Environ: e2.Environ, Cwd: t.TempDir(), FallbackIdentity: "pat"})
	if err != nil || r.Hooks.StaleMinutes() != 30 {
		t.Fatalf("absent: %d, %v", r.Hooks.StaleMinutes(), err)
	}
}
