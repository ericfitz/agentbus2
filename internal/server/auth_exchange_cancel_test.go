package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/token"
)

// TestTokenExchangeCancelIsNotAnOutage: a client that gives up during the
// provider's key-set fetch is logged as a cancel, not as an issuer outage;
// a real outage (jwks-down in TestTokenExchangeRefusals) keeps its reason.
func TestTokenExchangeCancelIsNotAnOutage(t *testing.T) {
	f := newFedEnv(t, 0, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil).Encode()
	req := newReq(t, "POST", "/v1/auth/token", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("status %d for a canceled request", rec.Code)
	}
	logs := f.log.String()
	if strings.Contains(logs, "issuer key set unavailable") {
		t.Errorf("a client cancel is logged as an issuer outage:\n%s", logs)
	}
	if !strings.Contains(logs, "canceled") {
		t.Errorf("cancel not logged as such:\n%s", logs)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// A key-set fetch that timed out wraps both ErrJWKSUnavailable and
// DeadlineExceeded: that is the provider's outage and is logged as one, not as
// the caller's cancel.
func TestTokenExchangeFetchTimeoutIsAnOutage(t *testing.T) {
	f := newFedEnv(t, 0, func(e *token.External) {
		e.Client = &http.Client{Transport: failingTransport{context.DeadlineExceeded}}
	})
	s, _, m := f.post(exchangeForm(f.prov.Mint(map[string]any{"sub": "g-1", "email": corpEric}), nil))
	if s != http.StatusServiceUnavailable || m["error"] != "temporarily_unavailable" {
		t.Fatalf("status %d %v", s, m)
	}
	logs := f.log.String()
	if !strings.Contains(logs, "issuer key set unavailable") || strings.Contains(logs, "canceled") {
		t.Errorf("a fetch timeout is not logged as an issuer outage:\n%s", logs)
	}
}
