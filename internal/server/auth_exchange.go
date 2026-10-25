package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
)

const (
	grantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenTypeAccess    = "urn:ietf:params:oauth:token-type:access_token"
	tokenTypeID        = "urn:ietf:params:oauth:token-type:id_token"
)

// peekAlgorithms are the algorithms the unverified read of the issuer claim
// parses; the verification proper applies the issuer's own allowlist.
var peekAlgorithms = []jose.SignatureAlgorithm{
	jose.EdDSA, jose.ES256, jose.ES384, jose.ES512, jose.RS256, jose.RS384, jose.RS512, jose.PS256, jose.PS384, jose.PS512,
}

// maxLogged bounds any logged string that derives from the request.
const maxLogged = 200

// clip cuts s to maxLogged bytes (on a rune boundary) so a hostile token
// cannot inflate the log.
func clip(s string) string {
	if len(s) <= maxLogged {
		return s
	}
	return strings.ToValidUTF8(s[:maxLogged], "") + "..."
}

// refuseExchange logs a refused exchange (issuer, reason and identity when
// known; never a token) and builds its response.
func (a *api) refuseExchange(issuer string, e oauthError, reason, identity string) gen.TokenResponseObject {
	attrs := []any{"issuer", clip(issuer), "reason", clip(reason), "code", e.Code}
	if identity != "" {
		attrs = append(attrs, "identity", clip(identity))
	}
	a.log.Warn("token exchange refused", attrs...)
	return e
}

// tokenExchange is the RFC 8693 grant for humans (spec 5.2, 8): a provider
// access token in, an agentbus2 human bearer out. The provider token is
// verified, never stored and never logged.
func (a *api) tokenExchange(ctx context.Context, b *gen.TokenFormdataRequestBody) gen.TokenResponseObject {
	iss := a.opts.Issuer
	bad := func(status int, code, desc string) gen.TokenResponseObject {
		return a.refuseExchange("", oauthFail(status, code, "%s", desc), desc, "")
	}
	if b.SubjectToken == nil || *b.SubjectToken == "" {
		return bad(http.StatusBadRequest, "invalid_request", "subject_token is required")
	}
	switch {
	case b.SubjectTokenType == nil || *b.SubjectTokenType == "":
		return bad(http.StatusBadRequest, "invalid_request", "subject_token_type is required")
	case *b.SubjectTokenType == tokenTypeID:
		return bad(http.StatusBadRequest, "invalid_request", "ID tokens are not accepted; present the provider's access token")
	case *b.SubjectTokenType != tokenTypeAccess:
		return bad(http.StatusBadRequest, "invalid_request", "subject_token_type must be "+tokenTypeAccess)
	}
	if b.RequestedTokenType != nil && *b.RequestedTokenType != "" && *b.RequestedTokenType != tokenTypeAccess {
		return bad(http.StatusBadRequest, "invalid_request", "requested_token_type must be "+tokenTypeAccess)
	}
	for _, v := range []*string{b.Audience, b.Resource} {
		if v != nil && *v != "" && *v != iss.URL() {
			return bad(http.StatusBadRequest, "invalid_target", "audience and resource, when set, must be "+iss.URL())
		}
	}
	if (b.ActorToken != nil && *b.ActorToken != "") || (b.ActorTokenType != nil && *b.ActorTokenType != "") {
		return bad(http.StatusBadRequest, "invalid_request", "actor_token is not supported")
	}

	raw := *b.SubjectToken
	claimed, ok := unverifiedIssuer(raw)
	if !ok {
		return bad(http.StatusBadRequest, "invalid_request", "subject_token is not a valid token")
	}
	var ext *token.External
	for _, e := range a.opts.External {
		if e != nil && e.Issuer == claimed {
			ext = e
			break
		}
	}
	if ext == nil {
		return a.refuseExchange(claimed, oauthFail(http.StatusBadRequest, "invalid_request", "unknown issuer"), "unknown issuer", "")
	}
	fed, err := ext.Verify(ctx, raw)
	switch {
	case !errors.Is(err, token.ErrJWKSUnavailable) && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)):
		// The caller gave up or ran out of time; the provider is not known to be down.
		return a.refuseExchange(ext.Name, oauthFail(http.StatusServiceUnavailable, "temporarily_unavailable", "the request was canceled or timed out"),
			"request canceled or timed out while verifying", "")
	case errors.Is(err, token.ErrJWKSUnavailable):
		return a.refuseExchange(ext.Name, oauthFail(http.StatusServiceUnavailable, "temporarily_unavailable", "the issuer's key set is unavailable"),
			"issuer key set unavailable", "")
	case errors.Is(err, token.ErrNoEmail):
		return a.refuseExchange(ext.Name, oauthFail(http.StatusBadRequest, "invalid_request", "subject_token has no email claim"),
			"no email claim", "")
	case err != nil:
		return a.refuseExchange(ext.Name, oauthFail(http.StatusBadRequest, "invalid_request", "subject_token verification failed"),
			"verification failed: "+err.Error(), "")
	}

	res, err := identities.Service{Core: a.core}.SignInHuman(ctx, identities.HumanClaims{
		IssuerName: ext.Name, Issuer: ext.Issuer, Email: fed.Email, Groups: fed.Groups, Subject: fed.Subject,
	})
	if err != nil {
		oe := oauthFromBus(err, true)
		// The email claim is the identity the sign-in aimed at; it is not a token.
		who, _ := identities.NormalizeEmail(fed.Email)
		return a.refuseExchange(ext.Name, oe, string(bus.CodeOf(err))+": "+oe.Description, who)
	}
	clientID := fed.ClientID
	if clientID == "" {
		clientID = fed.Subject
	}
	raw, exp, err := iss.Mint(token.MintInput{Identity: res.Identity, Kind: token.KindHuman, ClientID: clientID, Lifetime: ext.EffectiveHumanLifetime()})
	if err != nil {
		return a.refuseExchange(ext.Name, oauthFromBus(bus.Internal(err), true), "mint failed", res.Identity)
	}
	a.log.Info("token exchange", "issuer", ext.Name, "identity", res.Identity, "created", res.Created)
	typ := tokenTypeAccess
	return noStoreToken{gen.Token200JSONResponse{
		AccessToken:     raw,
		TokenType:       "Bearer",
		ExpiresIn:       int64(exp.Sub(a.core.Now()).Seconds()),
		IssuedTokenType: &typ,
	}}
}

// unverifiedIssuer reads the iss claim of a compact JWT without verifying it,
// only to choose the configured issuer that then verifies the token.
func unverifiedIssuer(raw string) (string, bool) {
	if strings.Count(raw, ".") != 2 {
		return "", false
	}
	tok, err := jwt.ParseSigned(raw, peekAlgorithms)
	if err != nil {
		return "", false
	}
	var c struct {
		Issuer string `json:"iss"`
	}
	if err := tok.UnsafeClaimsWithoutVerification(&c); err != nil || c.Issuer == "" {
		return "", false
	}
	return c.Issuer, true
}
