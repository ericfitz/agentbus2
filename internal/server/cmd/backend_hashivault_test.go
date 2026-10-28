//go:build hashivault

package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// With the hashivault tag the hashivault scheme opens its provider; every
// other scheme is still refused with its own tag. Nothing here reaches a
// network: New validates the URI only.
func TestHashivaultBackendIsRegistered(t *testing.T) {
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	t.Setenv("VAULT_TOKEN", "t0ken-unused")
	p, err := openKeyProvider(config.Config{SigningKeyURI: "hashivault://agentbus2"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "hashivault" {
		t.Fatalf("provider %q", p.Name())
	}
	// A malformed key for the backend is refused, not accepted lazily; the
	// error does not quote the URI.
	_, err = openKeyProvider(config.Config{SigningKeyURI: "hashivault://user:t0ken@a/b"})
	if err == nil || strings.Contains(err.Error(), "t0ken") {
		t.Fatalf("a malformed hashivault URI: err %v", err)
	}
	for _, scheme := range keyprovider.Schemes() {
		if scheme == "hashivault" {
			continue
		}
		if _, ok := keyBackends[scheme]; ok {
			continue // another tag is on too
		}
		_, err := openKeyProvider(config.Config{SigningKeyURI: scheme + "://x"})
		want := "the " + scheme + " backend is not in this build (build with -tags " + scheme + ")"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", scheme, err, want)
		}
	}
}

// A hashivault URI with no usable Vault environment is a configuration error
// for the start and for rotate: exit 2, like the other backends.
func TestHashivaultUnsetEnvironmentExits2(t *testing.T) {
	for _, v := range []string{"VAULT_ADDR", "BAO_ADDR", "VAULT_TOKEN", "BAO_TOKEN"} {
		t.Setenv(v, "")
	}
	const uri = "signing_key_uri=hashivault://agentbus2"
	db := filepath.Join(t.TempDir(), "ab2.db")
	if code, _, stderr := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", uri); code != 2 || !strings.Contains(stderr, "hashivault: VAULT_ADDR") {
		t.Fatalf("start: exit %d, stderr %q; want 2", code, stderr)
	}
	e := newAdminEnv(t)
	if code, _, errOut := e.run("-set", uri, "signing-key", "rotate"); code != 2 || !strings.Contains(errOut, "hashivault: VAULT_ADDR") {
		t.Fatalf("rotate: exit %d, stderr %q; want 2", code, errOut)
	}
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	if code, _, stderr := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", uri); code != 2 || !strings.Contains(stderr, "VAULT_TOKEN") {
		t.Fatalf("no token: exit %d, stderr %q; want 2", code, stderr)
	}
	t.Setenv("VAULT_ADDR", "ftp://x")
	t.Setenv("VAULT_TOKEN", "t0ken")
	if code, _, stderr := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", uri); code != 2 || strings.Contains(stderr, "t0ken") {
		t.Fatalf("bad address: exit %d, stderr %q; want 2", code, stderr)
	}
}
