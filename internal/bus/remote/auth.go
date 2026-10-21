package remote

import (
	"context"
	"crypto"
	"encoding/json"
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
	return RegisterResult{Identity: g.Identity, KID: g.Kid, Algorithm: keys.Algorithm(g.Algorithm), Created: g.Created}, nil
}

// AccessToken is a minted bearer and its lifetime.
type AccessToken struct {
	Token     string
	ExpiresIn time.Duration
}

// Token exchanges a signed client assertion for a bearer (private_key_jwt).
func Token(ctx context.Context, c *gen.ClientWithResponses, clientAssertion string) (AccessToken, error) {
	r, err := c.TokenWithFormdataBodyWithResponse(ctx, gen.TokenFormdataRequestBody{
		GrantType:           gen.ClientCredentials,
		ClientAssertionType: gen.TokenRequestClientAssertionType(assertion.Type),
		ClientAssertion:     clientAssertion,
	})
	if err != nil {
		return AccessToken{}, errorOf(ctx, 0, nil, nil, err)
	}
	if err := errorOf(ctx, r.StatusCode(), r.ApplicationproblemJSONDefault, r.Body, nil); err != nil {
		return AccessToken{}, err
	}
	if r.JSON200 == nil {
		return AccessToken{}, unparsable(r.StatusCode())
	}
	return AccessToken{Token: r.JSON200.AccessToken, ExpiresIn: time.Duration(r.JSON200.ExpiresIn) * time.Second}, nil
}

// WithBearer sets Authorization: Bearer <token> on every request.
func WithBearer(token string) gen.RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}
