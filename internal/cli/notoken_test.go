package cli

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/credential"
)

var jwtShape = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// assertNoTokenOnStderr proves a human sign-in leaked no credential to
// stderr: the cached bearer and provider refresh token (both must exist, so
// the check is not vacuous) are absent, and nothing shaped like a JWT (the
// provider's access token is one, and is not cached) appears either.
func assertNoTokenOnStderr(t *testing.T, e *testEnv, profileName, stderr string) {
	t.Helper()
	s, ok, err := (credential.HumanCache{Dir: filepath.Join(e.DataDir, "tokens", profileName)}).Load()
	if err != nil || !ok {
		t.Fatalf("no human session to compare against: ok=%v err=%v", ok, err)
	}
	if s.AccessToken == "" || s.ProviderRefreshToken == "" {
		t.Fatalf("session lacks a bearer or a provider refresh token (%v, %v); the check would be vacuous",
			s.AccessToken != "", s.ProviderRefreshToken != "")
	}
	for name, secret := range map[string]string{"bearer": s.AccessToken, "provider refresh token": s.ProviderRefreshToken} {
		if strings.Contains(stderr, secret) {
			t.Errorf("stderr carries the %s", name)
		}
	}
	if m := jwtShape.FindString(stderr); m != "" {
		t.Errorf("stderr carries a JWT-shaped string (%d bytes)", len(m))
	}
}
