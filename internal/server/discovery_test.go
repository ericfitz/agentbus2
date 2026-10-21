package server

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ericfitz/agentbus2/internal/version"
)

func TestWellKnownPointsAtDiscover(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("").WellKnownWithResponse(context.Background())
	if err != nil || r.JSON200 == nil || r.JSON200.Discover != "/v1/discover" {
		t.Fatalf("%v %v", r, err)
	}
}

func TestDiscoverAgreesWithDocument(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("").DiscoverWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	d := r.JSON200
	schemes := Spec().Components.SecuritySchemes
	if len(d.AuthMethods) != len(schemes) {
		t.Fatalf("auth_methods %d, schemes %d", len(d.AuthMethods), len(schemes))
	}
	const oidc = "http://inproc/.well-known/openid-configuration"
	for _, m := range d.AuthMethods {
		ref, ok := schemes[m.Name]
		if !ok || ref.Value.Type != m.Type {
			t.Errorf("auth method %+v not in the document", m)
		}
		if m.Type == "openIdConnect" && (m.OpenidConfiguration == nil || *m.OpenidConfiguration != oidc) {
			t.Errorf("oidc entry %+v", m)
		}
		if m.Type == "http" && (m.Scheme == nil || *m.Scheme != "bearer") {
			t.Errorf("bearer entry %+v", m)
		}
	}
	if d.OpenidConfiguration == nil || *d.OpenidConfiguration != oidc {
		t.Errorf("top-level openid_configuration %v", d.OpenidConfiguration)
	}
	if !slices.Equal(d.ApiVersions, []string{"v1"}) || d.ServerVersion != version.Version {
		t.Errorf("versions %v %q", d.ApiVersions, d.ServerVersion)
	}
	if d.Issuers == nil || len(d.Issuers) != 0 {
		t.Errorf("issuers %#v", d.Issuers)
	}
}

func TestCapabilitiesComputed(t *testing.T) {
	if got := Capabilities(newEnv(t, envOptions{}).core); !slices.Equal(got, []string{"long_poll"}) {
		t.Errorf("no embedder: %v", got)
	}
	if got := Capabilities(newEnv(t, envOptions{embedder: true}).core); !slices.Equal(got, []string{"long_poll", "semantic_search"}) {
		t.Errorf("embedder: %v", got)
	}
}

// Decision (Eric, 2026-10-21): Milestone 4 item 2
func TestOpenIDConfigurationNamesMilestone5Paths(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("").OpenidConfigurationWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	c := r.JSON200
	if c.Issuer != "http://inproc" || c.TokenEndpoint != c.Issuer+"/v1/auth/token" || c.JwksUri != c.Issuer+"/v1/auth/jwks" {
		t.Errorf("%+v", c)
	}
	if !slices.Equal(c.TokenEndpointAuthMethodsSupported, []string{"private_key_jwt"}) ||
		!slices.Equal(c.TokenEndpointAuthSigningAlgValuesSupported, []string{"EdDSA", "ES256"}) {
		t.Errorf("%+v", c)
	}
	for _, path := range []string{"/v1/auth/token", "/v1/auth/jwks"} {
		resp := e.raw("", newReq(t, "GET", path, nil))
		if p := readProblem(t, resp); resp.StatusCode != 404 || string(p.Code) != "not_found" {
			t.Errorf("%s: %d %+v", path, resp.StatusCode, p)
		}
	}
}

func TestBaseURLHonorsTLS(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.test/x", nil)
	if got := baseURL(r); got != "http://example.test" {
		t.Errorf("plain: %q", got)
	}
	r.TLS = &tls.ConnectionState{}
	if got := baseURL(r); got != "https://example.test" {
		t.Errorf("tls: %q", got)
	}
}
