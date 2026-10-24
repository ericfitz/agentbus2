package remote

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	ab2bus "github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/gen"
)

// RegisterResult is what the server stored for a registration.
type RegisterResult struct {
	Identity, KID string
	Algorithm     keys.Algorithm
	Created       bool
}

// Register registers pub as a credential of identity. It is a pre-identity
// call, so it is a function on the generated client rather than a Bus method.
// enrollmentToken is sent only when non-empty.
func Register(ctx context.Context, c *gen.ClientWithResponses, identity string, pub crypto.PublicKey, enrollmentToken string) (RegisterResult, error) {
	kid, err := keys.KID(pub)
	if err != nil {
		return RegisterResult{}, &ab2bus.Error{Code: ab2bus.CodeValidation, Message: ab2bus.TruncateErrorMessage(err.Error())}
	}
	jwk, err := keys.ToJWK(pub, kid)
	if err != nil {
		return RegisterResult{}, &ab2bus.Error{Code: ab2bus.CodeValidation, Message: ab2bus.TruncateErrorMessage(err.Error())}
	}
	raw, err := jwk.MarshalJSON()
	if err != nil {
		return RegisterResult{}, &ab2bus.Error{Code: ab2bus.CodeInternal, Message: ab2bus.TruncateErrorMessage(err.Error())}
	}
	var m gen.JWK
	if err := json.Unmarshal(raw, &m); err != nil {
		return RegisterResult{}, &ab2bus.Error{Code: ab2bus.CodeInternal, Message: ab2bus.TruncateErrorMessage(err.Error())}
	}
	body := gen.RegisterRequest{Identity: identity, PublicKeyJwk: m}
	if enrollmentToken != "" {
		body.EnrollmentToken = &enrollmentToken
	}
	r, err := c.RegisterWithResponse(ctx, body)
	if err != nil {
		return RegisterResult{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return RegisterResult{}, err
	}
	if r.JSON201 == nil {
		return RegisterResult{}, unparsable(r.StatusCode())
	}
	g := r.JSON201
	alg := keys.Algorithm(g.Algorithm)
	if alg != keys.EdDSA && alg != keys.ES256 {
		return RegisterResult{}, &ab2bus.Error{Code: ab2bus.CodeInternal, Message: ab2bus.TruncateErrorMessage(fmt.Sprintf("HTTP %d: unrecognized algorithm %q", r.StatusCode(), g.Algorithm))}
	}
	return RegisterResult{Identity: g.Identity, KID: g.Kid, Algorithm: alg, Created: g.Created}, nil
}

// AccessToken is a minted bearer and its lifetime.
type AccessToken struct {
	Token     string
	ExpiresIn time.Duration
}

// Token exchanges a signed client assertion for a bearer (private_key_jwt).
func Token(ctx context.Context, c *gen.ClientWithResponses, clientAssertion string) (AccessToken, error) {
	assertionType := assertion.Type
	return tokenRequest(ctx, c, gen.TokenFormdataRequestBody{
		GrantType:           "client_credentials",
		ClientAssertionType: &assertionType,
		ClientAssertion:     &clientAssertion,
	})
}

// TokenExchange exchanges a corporate provider's access token for a human
// bearer (RFC 8693; spec 5.2). Errors map as Token's do.
func TokenExchange(ctx context.Context, c *gen.ClientWithResponses, subjectToken string) (AccessToken, error) {
	subjectType := tokenTypeAccess
	return tokenRequest(ctx, c, gen.TokenFormdataRequestBody{
		GrantType:        grantTokenExchange,
		SubjectToken:     &subjectToken,
		SubjectTokenType: &subjectType,
	})
}

func tokenRequest(ctx context.Context, c *gen.ClientWithResponses, body gen.TokenFormdataRequestBody) (AccessToken, error) {
	r, err := c.TokenWithFormdataBodyWithResponse(ctx, body)
	if err != nil {
		return AccessToken{}, errorOf(ctx, 0, nil, nil, err)
	}
	if r.StatusCode() >= 300 {
		if e := oauthErrorOf(r); e != nil {
			return AccessToken{}, e
		}
		return AccessToken{}, errorOf(ctx, r.StatusCode(), nil, r.Body, nil)
	}
	if r.JSON200 == nil {
		return AccessToken{}, unparsable(r.StatusCode())
	}
	return AccessToken{Token: r.JSON200.AccessToken, ExpiresIn: time.Duration(r.JSON200.ExpiresIn) * time.Second}, nil
}

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
)

// oauthErrorOf maps the RFC 6749 section 5.2 body of a refused token request
// to a bus error, or returns nil when the response carries none (a proxy's
// page, a problem body), which the caller maps by status.
func oauthErrorOf(r *gen.TokenResponse) error {
	var o *gen.OAuthError
	switch {
	case r.JSON400 != nil:
		o = r.JSON400
	case r.JSON401 != nil:
		o = r.JSON401
	case r.JSON500 != nil:
		o = r.JSON500
	case r.JSON503 != nil:
		o = r.JSON503
	}
	if o == nil || o.Error == "" {
		return nil
	}
	msg := o.Error
	if o.ErrorDescription != nil && *o.ErrorDescription != "" {
		msg = *o.ErrorDescription
	}
	e := &ab2bus.Error{Message: ab2bus.TruncateErrorMessage(msg)}
	switch o.Error {
	case "invalid_client", "invalid_grant":
		e.Code = ab2bus.CodeUnauthenticated
	case "access_denied":
		e.Code = ab2bus.CodeForbidden
	case "invalid_request", "unsupported_grant_type", "invalid_target", "invalid_scope":
		e.Code = ab2bus.CodeValidation
	case "temporarily_unavailable":
		e.Code, e.Retryable = ab2bus.CodeUnavailable, true
	case "server_error":
		e.Code = ab2bus.CodeInternal
	default:
		e.Code = ab2bus.CodeInternal
		e.Message = ab2bus.TruncateErrorMessage(fmt.Sprintf("unrecognized OAuth error %q: %s", o.Error, msg))
	}
	return e
}

// WithBearer sets Authorization: Bearer <token> on every request.
func WithBearer(token string) gen.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}
