//go:build azurekms

package cmd

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// With the azurekms tag the azurekms scheme opens its provider; every other scheme
// is refused with its own tag (setBackend removes it, so the check holds when
// several tags are on). Nothing here reaches a network or Application Default
// Credentials: New validates the URI only.
func TestAzurekmsBackendIsRegistered(t *testing.T) {
	const good = "azurekms://kv-test.vault.azure.net/signing-key"
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "prod")
	p, err := openKeyProvider(config.Config{SigningKeyURI: good})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "azurekms" {
		t.Fatalf("provider %q", p.Name())
	}
	// A malformed key for the backend is refused, not accepted lazily; the
	// error does not quote the URI.
	_, err = openKeyProvider(config.Config{SigningKeyURI: "azurekms://us3r:t0ken@kv-test.vault.azure.net/signing-key"})
	if err == nil || strings.Contains(err.Error(), "t0ken") || strings.Contains(err.Error(), "us3r") {
		t.Fatalf("a malformed azurekms URI: err %v", err)
	}
	// An unset credential selector is a configuration error here, so the
	// server exits 2 (not ErrUnavailable, which exits 1).
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "")
	if _, err = openKeyProvider(config.Config{SigningKeyURI: good}); err == nil || errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("an unset AZURE_TOKEN_CREDENTIALS: err %v", err)
	}
	for _, scheme := range keyprovider.Schemes() {
		if scheme == "azurekms" {
			continue
		}
		setBackend(t, scheme, nil)
		_, err := openKeyProvider(config.Config{SigningKeyURI: scheme + "://x"})
		want := "the " + scheme + " backend is not in this build (build with -tags " + scheme + ")"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err %v, want %q", scheme, err, want)
		}
	}
}

// An azurekms URI with AZURE_TOKEN_CREDENTIALS unset is a configuration error
// for the start and for rotate: exit 2.
func TestAzurekmsUnsetCredentialSelectorExits2(t *testing.T) {
	t.Setenv("AZURE_TOKEN_CREDENTIALS", "")
	const uri = "signing_key_uri=azurekms://kv-test.vault.azure.net/signing-key"
	db := filepath.Join(t.TempDir(), "ab2.db")
	if code, _, stderr := runOnce(nil, "-listen", "127.0.0.1:0", "-sqlite", db, "-set", uri); code != 2 || !strings.Contains(stderr, "AZURE_TOKEN_CREDENTIALS") {
		t.Fatalf("start: exit %d, stderr %q; want 2", code, stderr)
	}
	e := newAdminEnv(t)
	if code, _, errOut := e.run("-set", uri, "signing-key", "rotate"); code != 2 || !strings.Contains(errOut, "AZURE_TOKEN_CREDENTIALS") {
		t.Fatalf("rotate: exit %d, stderr %q; want 2", code, errOut)
	}
}
