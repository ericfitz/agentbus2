package server

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ericfitz/agentbus2/internal/auth/token"
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
		if m.Type == "http" && (m.Scheme == nil || *m.Scheme != "bearer") {
			t.Errorf("bearer entry %+v", m)
		}
	}
	if d.OpenidConfiguration == nil || *d.OpenidConfiguration != oidc {
		t.Errorf("top-level openid_configuration %v", d.OpenidConfiguration)
	}
	if d.ProtocolVersion != version.Protocol || d.ProtocolVersion != Spec().Info.Version {
		t.Errorf("protocol_version %q, version.Protocol %q, info.version %q", d.ProtocolVersion, version.Protocol, Spec().Info.Version)
	}
	if !slices.Equal(d.ApiVersions, []string{"v1"}) || d.ServerVersion != version.Version {
		t.Errorf("versions %v %q", d.ApiVersions, d.ServerVersion)
	}
	if d.Issuers == nil || len(d.Issuers) != 0 {
		t.Errorf("issuers %#v", d.Issuers)
	}
}

func TestDiscoverListsConfiguredExternalIssuers(t *testing.T) {
	ext := []*token.External{
		{Issuer: "https://idp.example", JWKSURI: "https://idp.example/jwks", Audience: "agentbus2", Name: "corp"},
		{Issuer: "https://other.example", JWKSURI: "https://other.example/jwks", Audience: "agentbus2", Name: "other"},
	}
	e := newEnv(t, envOptions{issuer: true, external: ext})
	r, err := e.client("").DiscoverWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	if !slices.Equal(r.JSON200.Issuers, []string{"https://idp.example", "https://other.example"}) {
		t.Errorf("issuers %v", r.JSON200.Issuers)
	}
}

func TestCapabilitiesComputed(t *testing.T) {
	if got := Capabilities(newEnv(t, envOptions{}).core); !slices.Equal(got, []string{"health", "long_poll"}) {
		t.Errorf("no embedder: %v", got)
	}
	if got := Capabilities(newEnv(t, envOptions{embedder: true}).core); !slices.Equal(got, []string{"health", "long_poll", "semantic_search"}) {
		t.Errorf("embedder: %v", got)
	}
}

func TestAuthorizationServerMetadata(t *testing.T) {
	e := newEnv(t, envOptions{})
	ctx := context.Background()
	r, err := e.client("").AuthorizationServerMetadataWithResponse(ctx)
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	c := r.JSON200
	if c.Issuer != "http://inproc" || c.TokenEndpoint != c.Issuer+"/v1/auth/token" || c.JwksUri != c.Issuer+"/v1/auth/jwks" {
		t.Errorf("%+v", c)
	}
	if !slices.Equal(c.TokenEndpointAuthMethodsSupported, []string{"private_key_jwt", "none"}) ||
		!slices.Equal(c.TokenEndpointAuthSigningAlgValuesSupported, []string{"EdDSA", "ES256"}) ||
		!slices.Equal(c.GrantTypesSupported, []string{"client_credentials", grantTokenExchange}) {
		t.Errorf("%+v", c)
	}
	if c.ResponseTypesSupported == nil || len(c.ResponseTypesSupported) != 0 {
		t.Errorf("response_types_supported %#v, want []", c.ResponseTypesSupported)
	}
	// Both well-known paths return the same document, with response_types_supported present.
	var bodies [2]string
	for i, path := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		resp := e.do("GET", path, "", "", "")
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		bodies[i] = readBody(t, resp)
	}
	if bodies[0] != bodies[1] {
		t.Errorf("documents differ:\n%s\n%s", bodies[0], bodies[1])
	}
	if !strings.Contains(bodies[0], `"response_types_supported":[]`) {
		t.Errorf("no empty response_types_supported: %s", bodies[0])
	}
}

func TestDiscoverHasNoOpenIDConnectScheme(t *testing.T) {
	e := newEnv(t, envOptions{})
	r, err := e.client("").DiscoverWithResponse(context.Background())
	if err != nil || r.JSON200 == nil {
		t.Fatalf("%v %v", r, err)
	}
	for _, m := range r.JSON200.AuthMethods {
		if m.Type == "openIdConnect" {
			t.Errorf("auth method %+v", m)
		}
	}
	if as := r.JSON200.AuthorizationServer; as == nil || !strings.HasSuffix(*as, "/.well-known/oauth-authorization-server") {
		t.Errorf("authorization_server %v", as)
	}
	for name := range Spec().Components.SecuritySchemes {
		if name == "oidc" {
			t.Error("oidc security scheme still in the document")
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
