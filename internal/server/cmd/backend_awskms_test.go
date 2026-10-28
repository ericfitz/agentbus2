//go:build awskms

package cmd

import (
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// With the awskms tag the awskms scheme opens its provider; every other
// scheme is still refused with its own tag.
func TestAwskmsBackendIsRegistered(t *testing.T) {
	p, err := openKeyProvider(config.Config{SigningKeyURI: "awskms:///alias/agentbus2"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "awskms" {
		t.Fatalf("provider %q", p.Name())
	}
	// A malformed key for the backend is refused, not accepted lazily.
	if _, err := openKeyProvider(config.Config{SigningKeyURI: "awskms:///not-a-key"}); err == nil {
		t.Fatal("a malformed awskms URI was accepted")
	}
	for _, scheme := range keyprovider.Schemes() {
		if scheme == "awskms" {
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
