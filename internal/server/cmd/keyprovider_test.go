package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/keyprovider"
	"github.com/ericfitz/agentbus2/internal/keyprovider/kmsfake"
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
	setBackend(t, "awskms", func(uri string) (keyprovider.Provider, error) {
		got = uri
		return stubProvider{}, nil
	})
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

// useFake registers f as the awskms backend for the test.
func useFake(t *testing.T, f *kmsfake.Fake) {
	t.Helper()
	setBackend(t, "awskms", func(string) (keyprovider.Provider, error) { return f, nil })
}

// Controller ruling 2: the first-start line names the scheme and key name
// only; the rest of the URI (a query may carry a token) never reaches the log.
func TestStartAdoptsExternalKeyAndPrintsNoURI(t *testing.T) {
	useFake(t, kmsfake.New("awskms"))
	db := filepath.Join(t.TempDir(), "ab2.db")
	args := []string{"-listen", "127.0.0.1:0", "-sqlite", db, "-set", "signing_key_uri=awskms://alias/agentbus2?token=s3cret"}
	first := runServer(t, args...)
	want := regexp.MustCompile(`adopted signing key ` + first.kid + ` \(awskms alias/agentbus2\)\n`)
	if !want.MatchString(first.stdout.String()) {
		t.Fatalf("stdout %q lacks the adopted line", first.stdout.String())
	}
	if out := first.stdout.String() + first.stderr.String(); strings.Contains(out, "s3cret") || strings.Contains(out, "awskms://") {
		t.Fatalf("the key URI leaked: %q", out)
	}
	if first.stop() != 0 {
		t.Fatal("first start did not exit 0")
	}
	// A second start loads the row: nothing adopted, same kid.
	second := runServer(t, args...)
	if strings.Contains(second.stdout.String(), "adopted signing key") || second.kid != first.kid {
		t.Fatalf("second start: kid %s (want %s), stdout %q", second.kid, first.kid, second.stdout.String())
	}
}

// Controller ruling 3: a provider runtime failure at start exits 1; a
// configuration error exits 2.
func TestStartExitCodesForProviderFailures(t *testing.T) {
	uriArg := []string{"-set", "signing_key_uri=awskms://alias/agentbus2"}
	run := func(t *testing.T) (int, string) {
		t.Helper()
		db := filepath.Join(t.TempDir(), "ab2.db")
		code, _, stderr := runOnce(nil, append([]string{"-listen", "127.0.0.1:0", "-sqlite", db}, uriArg...)...)
		return code, stderr
	}
	t.Run("first EnsureActive finds the service down", func(t *testing.T) {
		f := kmsfake.New("awskms")
		f.SetDown(true)
		useFake(t, f)
		if code, stderr := run(t); code != 1 || !strings.Contains(stderr, "signing key") {
			t.Fatalf("exit %d, stderr %q; want 1", code, stderr)
		}
	})
	t.Run("opening reports the service unreachable", func(t *testing.T) {
		setBackend(t, "awskms", func(string) (keyprovider.Provider, error) {
			return nil, fmt.Errorf("dial: %w", keyprovider.ErrUnavailable)
		})
		if code, stderr := run(t); code != 1 {
			t.Fatalf("exit %d, stderr %q; want 1", code, stderr)
		}
	})
	t.Run("a malformed URI is a configuration error", func(t *testing.T) {
		setBackend(t, "awskms", func(string) (keyprovider.Provider, error) { return nil, errors.New("bad key URI") })
		if code, stderr := run(t); code != 2 || !strings.Contains(stderr, "bad key URI") {
			t.Fatalf("exit %d, stderr %q; want 2", code, stderr)
		}
	})
	t.Run("a backend not in this build is a configuration error", func(t *testing.T) {
		setBackend(t, "awskms", nil)
		if code, stderr := run(t); code != 2 || !strings.Contains(stderr, "not in this build") {
			t.Fatalf("exit %d, stderr %q; want 2", code, stderr)
		}
	})
}

// setBackend registers open for scheme (nil removes it) and restores what was
// there, so a tagged build's init registration survives the test.
func setBackend(t *testing.T, scheme string, open func(string) (keyprovider.Provider, error)) {
	t.Helper()
	prev, had := keyBackends[scheme]
	if open == nil {
		delete(keyBackends, scheme)
	} else {
		keyBackends[scheme] = open
	}
	t.Cleanup(func() {
		if had {
			keyBackends[scheme] = prev
		} else {
			delete(keyBackends, scheme)
		}
	})
}

// Rotate opens the provider like the start does: a configuration error is exit
// 2, a provider that reports the service down stays exit 1.
func TestAdminRotateExitCodesForProviderFailures(t *testing.T) {
	rotate := func(t *testing.T, uri string) (int, string) {
		t.Helper()
		e := newAdminEnv(t)
		code, _, errOut := e.run("-set", "signing_key_uri="+uri, "signing-key", "rotate")
		return code, errOut
	}
	t.Run("a backend not in this build", func(t *testing.T) {
		setBackend(t, "awskms", nil)
		if code, errOut := rotate(t, "awskms://alias/agentbus2"); code != 2 || !strings.Contains(errOut, "not in this build") {
			t.Fatalf("exit %d, stderr %q; want 2", code, errOut)
		}
	})
	t.Run("a malformed URI", func(t *testing.T) {
		setBackend(t, "awskms", func(string) (keyprovider.Provider, error) { return nil, errors.New("bad key URI") })
		if code, errOut := rotate(t, "awskms://alias/agentbus2"); code != 2 || !strings.Contains(errOut, "bad key URI") {
			t.Fatalf("exit %d, stderr %q; want 2", code, errOut)
		}
	})
	t.Run("the service is unreachable", func(t *testing.T) {
		setBackend(t, "awskms", func(string) (keyprovider.Provider, error) {
			return nil, fmt.Errorf("dial: %w", keyprovider.ErrUnavailable)
		})
		if code, errOut := rotate(t, "awskms://alias/agentbus2"); code != 1 {
			t.Fatalf("exit %d, stderr %q; want 1", code, errOut)
		}
	})
}
