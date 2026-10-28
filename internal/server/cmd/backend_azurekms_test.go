//go:build azurekms

package cmd

import (
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
