package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/server/config"
)

// open note: signing-keys 2
func TestOpenKeyProviderEmptyIsTheStoreProvider(t *testing.T) {
	p, err := openKeyProvider(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != keyprovider.NameStore {
		t.Fatalf("provider %q, want %q", p.Name(), keyprovider.NameStore)
	}
}

// In the default build no backend is registered, so every scheme is refused
// with its tag name.
func TestOpenKeyProviderRefusesSchemeWithoutBackend(t *testing.T) {
	for _, scheme := range keyprovider.Schemes() {
		if _, ok := keyBackends[scheme]; ok {
			continue // a tagged build registered it
		}
		_, err := openKeyProvider(config.Config{SigningKeyURI: scheme + "://x"})
		want := "signing_key_uri: the " + scheme + " backend is not in this build (build with -tags " + scheme + ")"
		if err == nil || err.Error() != want {
			t.Errorf("%s: err %v, want %q", scheme, err, want)
		}
	}
}

func TestOpenKeyProviderUsesRegisteredBackend(t *testing.T) {
	var got string
	keyBackends["awskms"] = func(uri string) (keyprovider.Provider, error) {
		got = uri
		return stubProvider{}, nil
	}
	t.Cleanup(func() { delete(keyBackends, "awskms") })
	p, err := openKeyProvider(config.Config{SigningKeyURI: "awskms://k"})
	if err != nil || p.Name() != "awskms" || got != "awskms://k" {
		t.Fatalf("provider %v, err %v, uri %q", p, err, got)
	}
}

func TestOpenKeyProviderRejectsUnknownScheme(t *testing.T) {
	_, err := openKeyProvider(config.Config{SigningKeyURI: "gcs://x"})
	if err == nil || !strings.Contains(err.Error(), "signing_key_uri") {
		t.Fatalf("err %v", err)
	}
}

func TestSigningSettings(t *testing.T) {
	cfg := config.Default()
	s := signingSettings(cfg)
	if s.Interval.Hours() != 720 || s.Cooldown.Seconds() != 3600 {
		t.Fatalf("defaults: %+v", s)
	}
	cfg.SigningKeyRotationHours = 0
	cfg.SigningKeyRotationCooldownSeconds = 5
	s = signingSettings(cfg)
	if s.Interval != 0 || s.Cooldown.Seconds() != 5 {
		t.Fatalf("off/5: %+v", s)
	}
}

// stubProvider stands in for an external backend (kmsfake arrives later).
type stubProvider struct{}

func (stubProvider) Name() string { return "awskms" }
func (stubProvider) Next(context.Context) (keyprovider.Key, error) {
	return keyprovider.Key{}, errors.New("stub: no key")
}
func (stubProvider) Signer(context.Context, keyprovider.Key) (keyprovider.Signer, error) {
	return nil, errors.New("stub: no signer")
}
