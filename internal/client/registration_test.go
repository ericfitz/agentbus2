package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/credential"
	"github.com/ericfitz/agentbus2/internal/profile"
	"github.com/ericfitz/agentbus2/internal/testkit"
)

func TestRegisterIdentityWithCredential(t *testing.T) {
	ctx := context.Background()
	base := startServer(t)
	e := testkit.NewProfileEnv(t)
	path, _ := writeKeyFile(t, keys.EdDSA)
	req := RegisterRequest{Options: Options{Environ: e.Environ, Cwd: t.TempDir(), Identity: "pat", Server: base, Credential: path}, Algorithm: keys.EdDSA}

	out, err := RegisterIdentity(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || !out.ProfileCreated || out.Identity != "pat" || out.Profile != "default" || out.Server != base {
		t.Fatalf("first run %+v", out)
	}
	cfg, err := profile.Load(filepath.Join(e.Config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Profiles["default"]; p.Mode != profile.ModeServer || p.Server != base || p.Identity != "pat" {
		t.Fatalf("profile %+v", p)
	}
	if _, err := os.Stat(filepath.Join(e.Config, "credentials")); !os.IsNotExist(err) {
		t.Fatalf("credentials directory exists: %v", err)
	}
	// Definition of Done D: a second run registers the same key again.
	out, err = RegisterIdentity(ctx, req)
	if err != nil || out.Created || out.ProfileCreated {
		t.Fatalf("second run %+v, %v", out, err)
	}
	// An explicit algorithm that differs from the file's is refused.
	req.Algorithm, req.AlgorithmExplicit = keys.ES256, true
	_, err = RegisterIdentity(ctx, req)
	var re *ResolveError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "the key in "+path+" is EdDSA, not ES256") {
		t.Fatalf("algorithm mismatch: %v", err)
	}
}

func TestRegisterIdentityNoCredentialFileLeaves(t *testing.T) {
	ctx := context.Background()
	base := startServer(t)
	e := testkit.NewProfileEnv(t)
	req := RegisterRequest{Options: Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: "lab", Identity: "pat", Server: base}, Algorithm: keys.EdDSA}
	out, err := RegisterIdentity(ctx, req)
	if err != nil || !out.Created || !out.ProfileCreated {
		t.Fatalf("%+v, %v", out, err)
	}
	cs := credential.Store{Dir: profile.CredentialDir(e.Config, "lab")}
	if !cs.Exists("pat") {
		t.Fatal("no credential saved")
	}
	if _, err := os.Stat(cs.PendingPath("pat")); !os.IsNotExist(err) {
		t.Fatalf("pending file remains: %v", err)
	}
}

// TestRegisterIdentityUsageErrors: every usage error is a *ResolveError with
// today's text.
func TestRegisterIdentityUsageErrors(t *testing.T) {
	e := testkit.NewProfileEnv(t)
	e.Save(t, profile.Profile{Name: "default", Mode: profile.ModeLocal}, profile.Profile{Name: "lab", Mode: profile.ModeServer, Server: "http://127.0.0.1:1"})
	cases := []struct {
		name string
		o    Options
		want string
	}{
		{"undefined without server", Options{Profile: "nope", Identity: "pat"}, `profile "nope" is not defined in `},
		{"local", Options{Profile: "default", Identity: "pat"}, "register applies to server profiles only; a local profile needs no registration"},
		{"server disagrees", Options{Profile: "lab", Identity: "pat", Server: "http://127.0.0.1:2"}, `profile "lab" uses server http://127.0.0.1:1; -server http://127.0.0.1:2 disagrees`},
		{"no identity", Options{Profile: "lab"}, "identity is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.o.Environ, c.o.Cwd = e.Environ, t.TempDir()
			_, err := RegisterIdentity(context.Background(), RegisterRequest{Options: c.o, Algorithm: keys.EdDSA})
			var re *ResolveError
			if !errors.As(err, &re) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v (%T), want a ResolveError containing %q", err, err, c.want)
			}
		})
	}
	t.Run("unknown algorithm", func(t *testing.T) {
		_, err := RegisterIdentity(context.Background(), RegisterRequest{Options: Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: "lab", Identity: "pat"}, Algorithm: "RS256"})
		var re *ResolveError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), "EdDSA or ES256") {
			t.Fatalf("err %v", err)
		}
	})
}

// TestRegisterIdentityPendingConflict: a pending key the server does not hold
// fails with a *PendingConflictError naming the pending file (Ruling 19: the
// hint text lives in PendingConflictError).
func TestRegisterIdentityPendingConflict(t *testing.T) {
	ctx := context.Background()
	base := startServer(t)
	e := testkit.NewProfileEnv(t)
	req := RegisterRequest{Options: Options{Environ: e.Environ, Cwd: t.TempDir(), Profile: "lab", Identity: "pat", Server: base}, Algorithm: keys.EdDSA}
	if _, err := RegisterIdentity(ctx, req); err != nil {
		t.Fatal(err)
	}
	cs := credential.Store{Dir: profile.CredentialDir(e.Config, "lab")}
	stale, _ := keys.Generate(keys.EdDSA)
	if err := cs.SavePending("pat", stale); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cs.Path("pat"), cs.Path("pat")+".keep"); err != nil {
		t.Fatal(err)
	}
	_, err := RegisterIdentity(ctx, req)
	var pc *PendingConflictError
	if !errors.As(err, &pc) {
		t.Fatalf("err %v (%T), want a *PendingConflictError", err, err)
	}
	if pc.Path != cs.PendingPath("pat") || pc.Identity != "pat" || pc.Err == nil || !errors.Is(err, pc.Err) {
		t.Fatalf("%+v", pc)
	}
	if !strings.Contains(pc.Hint(), cs.PendingPath("pat")) || !strings.Contains(pc.Hint(), "reset the credential for \"pat\"") {
		t.Fatalf("hint %q", pc.Hint())
	}
}
