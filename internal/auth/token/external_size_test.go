package token

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/keys"
)

// TestExternalJWKSSizeGuard isolates the size guard: the body is a valid key
// set padded with trailing whitespace (which the JSON parser accepts), so only
// the limit can refuse it. A body of exactly maxJWKSBytes verifies; one byte
// more is "too large".
func TestExternalJWKSSizeGuard(t *testing.T) {
	k := genKey(t, keys.EdDSA)
	valid, err := json.Marshal(newJWKS(t, k).set)
	if err != nil {
		t.Fatal(err)
	}
	padded := func(n int) string { return string(valid) + strings.Repeat(" ", n-len(valid)) }
	serve := func(body string) *External {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		t.Cleanup(srv.Close)
		return newExternal(&jwksServer{Server: srv}, &clock{t: t0})
	}
	if _, err := serve(padded(maxJWKSBytes)).Verify(context.Background(), extToken(t, k, nil)); err != nil {
		t.Errorf("body of exactly %d bytes: %v, want it accepted", maxJWKSBytes, err)
	}
	_, err = serve(padded(maxJWKSBytes+1)).Verify(context.Background(), extToken(t, k, nil))
	if !errors.Is(err, ErrJWKSUnavailable) || !strings.Contains(err.Error(), "too large") {
		t.Errorf("body of %d bytes: %v, want ErrJWKSUnavailable (too large)", maxJWKSBytes+1, err)
	}
}
