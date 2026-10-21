package server

import (
	"context"
	"net/http"
	"sort"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/gen"
)

type baseURLKey struct{}

// baseURL is the request's scheme and host: https when r.TLS is set.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func baseURLFrom(ctx context.Context) string {
	s, _ := ctx.Value(baseURLKey{}).(string)
	return s
}

// Capabilities is what this server offers, sorted: long_poll always,
// semantic_search when an embedder is configured (choice 7).
func Capabilities(c *core.Core) []string {
	caps := []string{"long_poll"}
	if c.Embedder() != nil {
		caps = append(caps, "semantic_search")
	}
	sort.Strings(caps)
	return caps
}

const openidConfigurationPath = "/.well-known/openid-configuration"

// WellKnown answers GET /.well-known/agentbus2.
func (a *api) WellKnown(context.Context, gen.WellKnownRequestObject) (gen.WellKnownResponseObject, error) {
	return gen.WellKnown200JSONResponse{Discover: "/v1/discover"}, nil
}

// OpenidConfiguration answers GET /.well-known/openid-configuration
// (Decisions 55, 70, 84, 91). The token and jwks paths arrive in Milestone 5
// (Decision (Eric, 2026-10-21): Milestone 4 item 2) and answer 404 until then.
func (a *api) OpenidConfiguration(ctx context.Context, _ gen.OpenidConfigurationRequestObject) (gen.OpenidConfigurationResponseObject, error) {
	issuer := baseURLFrom(ctx)
	return gen.OpenidConfiguration200JSONResponse{
		Issuer:                            issuer,
		TokenEndpoint:                     issuer + "/v1/auth/token",
		JwksUri:                           issuer + "/v1/auth/jwks",
		TokenEndpointAuthMethodsSupported: []string{"private_key_jwt"},
		TokenEndpointAuthSigningAlgValuesSupported: []string{"EdDSA", "ES256"},
	}, nil
}

// Discover answers GET /v1/discover, computed from the embedded document
// (Decision 65 by construction).
func (a *api) Discover(ctx context.Context, _ gen.DiscoverRequestObject) (gen.DiscoverResponseObject, error) {
	oidcURL := baseURLFrom(ctx) + openidConfigurationPath
	schemes := Spec().Components.SecuritySchemes
	names := make([]string, 0, len(schemes))
	for n := range schemes {
		names = append(names, n)
	}
	sort.Strings(names)
	methods := make([]gen.AuthMethod, 0, len(names))
	for _, n := range names {
		s := schemes[n].Value
		m := gen.AuthMethod{Name: n, Type: s.Type}
		if s.Scheme != "" {
			sc := s.Scheme
			m.Scheme = &sc
		}
		if s.BearerFormat != "" {
			bf := s.BearerFormat
			m.BearerFormat = &bf
		}
		if s.Type == "openIdConnect" {
			u := oidcURL
			m.OpenidConfiguration = &u
		}
		methods = append(methods, m)
	}
	issuers := append([]string{}, a.opts.Issuers...)
	return gen.Discover200JSONResponse{
		ApiVersions:         []string{"v1"},
		AuthMethods:         methods,
		Capabilities:        Capabilities(a.core),
		Issuers:             issuers,
		OpenidConfiguration: &oidcURL,
		ServerVersion:       a.version,
	}, nil
}
