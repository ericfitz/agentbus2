//go:build hashivault

package cmd

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// With the hashivault tag the hashivault scheme opens its provider; every
// other scheme is still refused with its own tag. Nothing here reaches a
// network: New validates the URI only.
func TestHashivaultBackendIsRegistered(t *testing.T) {
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
