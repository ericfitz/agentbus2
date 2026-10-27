package server

import (
	"context"
	"net/http"
	"sort"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/version"
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

// Capabilities is what this server offers, sorted: health and long_poll always,
// semantic_search when an embedder is configured (choice 7).
func Capabilities(c *core.Core) []string {
	caps := []string{"health", "long_poll"}
	if c.Embedder() != nil {
		caps = append(caps, "semantic_search")
	}
	sort.Strings(caps)
	return caps
}

const (
	openidConfigurationPath = "/.well-known/openid-configuration"
	metadataPath            = "/.well-known/oauth-authorization-server"
)

// WellKnown answers GET /.well-known/agentbus2.
func (a *api) WellKnown(context.Context, gen.WellKnownRequestObject) (gen.WellKnownResponseObject, error) {
	return gen.WellKnown200JSONResponse{Discover: "/v1/discover"}, nil
}

// metadata is the RFC 8414 document. The issuer is the configured one when
// set (Decision (Eric, 2026-10-21), item 3), never derived from the request
// Host then; without an issuer it is the request's base URL.
func (a *api) metadata(ctx context.Context) gen.AuthorizationServerMetadata {
	issuer := baseURLFrom(ctx)
	if a.opts.Issuer != nil {
		issuer = a.opts.Issuer.URL()
	}
	return gen.AuthorizationServerMetadata{
		Issuer:                            issuer,
		TokenEndpoint:                     issuer + "/v1/auth/token",
		JwksUri:                           issuer + "/v1/auth/jwks",
		ResponseTypesSupported:            []string{},
		GrantTypesSupported:               []string{"client_credentials", grantTokenExchange},
		TokenEndpointAuthMethodsSupported: []string{"private_key_jwt", "none"},
		TokenEndpointAuthSigningAlgValuesSupported: []string{"EdDSA", "ES256"},
	}
}

// AuthorizationServerMetadata answers GET /.well-known/oauth-authorization-server
// (RFC 8414; Decisions 55, 70, 84, 91).
func (a *api) AuthorizationServerMetadata(ctx context.Context, _ gen.AuthorizationServerMetadataRequestObject) (gen.AuthorizationServerMetadataResponseObject, error) {
	return gen.AuthorizationServerMetadata200JSONResponse(a.metadata(ctx)), nil
}

// OpenidConfiguration answers GET /.well-known/openid-configuration with the
// same document, for clients that predate the RFC 8414 path.
func (a *api) OpenidConfiguration(ctx context.Context, _ gen.OpenidConfigurationRequestObject) (gen.OpenidConfigurationResponseObject, error) {
	return gen.OpenidConfiguration200JSONResponse(a.metadata(ctx)), nil
}

// Discover answers GET /v1/discover, computed from the embedded document
// (Decision 65 by construction).
func (a *api) Discover(ctx context.Context, _ gen.DiscoverRequestObject) (gen.DiscoverResponseObject, error) {
	base := baseURLFrom(ctx)
	if a.opts.Issuer != nil {
		base = a.opts.Issuer.URL()
	}
	oidcURL := base + openidConfigurationPath
	asURL := base + metadataPath
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
		methods = append(methods, m)
	}
	issuers := make([]string, 0, len(a.opts.External))
	for _, e := range a.opts.External {
		if e != nil {
			issuers = append(issuers, e.Issuer)
		}
	}
	return gen.Discover200JSONResponse{
		ApiVersions:         []string{"v1"},
		AuthMethods:         methods,
		AuthorizationServer: &asURL,
		Capabilities:        Capabilities(a.core),
		Issuers:             issuers,
		OpenidConfiguration: &oidcURL,
		ProtocolVersion:     version.Protocol,
		ServerVersion:       a.version,
	}, nil
}
