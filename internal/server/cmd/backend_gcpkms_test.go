//go:build gcpkms

package cmd

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// With the gcpkms tag the gcpkms scheme opens its provider; every other scheme
// is refused with its own tag (setBackend removes it, so the check holds when
// several tags are on). Nothing here reaches a network or Application Default
// Credentials: New validates the URI only.
func TestGcpkmsBackendIsRegistered(t *testing.T) {
	const good = "gcpkms://projects/p/locations/global/keyRings/r/cryptoKeys/k"
	p, err := openKeyProvider(config.Config{SigningKeyURI: good})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "gcpkms" {
		t.Fatalf("provider %q", p.Name())
	}
	// A malformed key for the backend is refused, not accepted lazily; the
	// error does not quote the URI.
	_, err = openKeyProvider(config.Config{SigningKeyURI: "gcpkms://us3r:t0ken@projects/p/locations/l/keyRings/r/cryptoKeys/k"})
	if err == nil || strings.Contains(err.Error(), "t0ken") || strings.Contains(err.Error(), "us3r") {
		t.Fatalf("a malformed gcpkms URI: err %v", err)
	}
	for _, scheme := range keyprovider.Schemes() {
		if scheme == "gcpkms" {
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
