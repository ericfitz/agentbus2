package credential

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverReadsRFC8414Path(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"issuer":"http://i","token_endpoint":"http://i/v1/auth/token","jwks_uri":"http://i/v1/auth/jwks","other":1}`))
	}))
	defer srv.Close()
	for _, server := range []string{srv.URL, srv.URL + "/"} {
		d, err := Discover(context.Background(), nil, server)
		if err != nil {
			t.Fatal(err)
		}
		if d.Issuer != "http://i" || d.TokenEndpoint != "http://i/v1/auth/token" || d.JWKSURI != "http://i/v1/auth/jwks" {
			t.Errorf("document %+v", d)
		}
	}
}

func TestDiscoverRejects(t *testing.T) {
	var body string
	var status = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"404":               {http.StatusNotFound, "no"},
		"500":               {http.StatusInternalServerError, "no"},
		"no token_endpoint": {http.StatusOK, `{"issuer":"x"}`},
		"no issuer":         {http.StatusOK, `{"token_endpoint":"http://i/v1/auth/token"}`},
		"not json":          {http.StatusOK, `<html>`},
	} {
		status, body = tc.status, tc.body
		_, err := Discover(context.Background(), nil, srv.URL)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), srv.URL+"/.well-known/oauth-authorization-server") {
			t.Errorf("%s: error does not name the URL: %v", name, err)
		}
	}
}

func TestDiscoverRequiresIssuer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token_endpoint":"http://i/v1/auth/token","jwks_uri":"http://i/v1/auth/jwks"}`))
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), nil, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "no issuer") {
		t.Fatalf("err = %v, want no issuer", err)
	}
}
