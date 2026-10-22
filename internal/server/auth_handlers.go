package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/ericfitz/agentbus2/internal/auth/assertion"
	"github.com/ericfitz/agentbus2/internal/auth/keys"
	"github.com/ericfitz/agentbus2/internal/auth/token"
	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
	"github.com/ericfitz/agentbus2/internal/gen"
	"github.com/ericfitz/agentbus2/internal/store"
)

func issuerUnavailable() error {
	return core.E(bus.CodeUnavailable, "token issuer not configured")
}

// noStoreToken adds the RFC 6749 section 5.1 cache headers to a 200 token
// response.
type noStoreToken struct{ gen.Token200JSONResponse }

func (r noStoreToken) VisitTokenResponse(w http.ResponseWriter) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	return r.Token200JSONResponse.VisitTokenResponse(w)
}

// Token exchanges a private_key_jwt client assertion for a bearer (Decisions
// 19, 70, 84).
func (a *api) Token(ctx context.Context, req gen.TokenRequestObject) (gen.TokenResponseObject, error) {
	iss := a.opts.Issuer
	if iss == nil {
		return nil, issuerUnavailable()
	}
	if req.Body == nil {
		return nil, core.E(bus.CodeValidation, "request body is required")
	}
	p, err := assertion.Parse(req.Body.ClientAssertion)
	if err != nil {
		return nil, core.E(bus.CodeValidation, "client assertion: %s", err)
	}
	pub, _, err := identities.Service{Core: a.core}.PublicKey(ctx, p.Identity, p.KID)
	if err != nil {
		if bus.CodeOf(err) == bus.CodeNotFound {
			return nil, core.E(bus.CodeUnauthenticated, "unknown identity or key")
		}
		return nil, err
	}
	now := a.core.Now()
	if err := p.Verify(pub, iss.TokenEndpoint(), now, token.Leeway); err != nil {
		return nil, core.E(bus.CodeUnauthenticated, "%s", err)
	}
	// Verify accepts the assertion until exp plus the leeway inclusive, and
	// the expiry job deletes rows with expires_at < now, so the jti is kept
	// until one second past exp+Leeway: forgetting it at exp would let a
	// replay inside the leeway mint a second bearer. The insert is the whole
	// transaction (Decision 26: no side effects); replicas racing on one jti
	// serialize on the primary key and exactly one commits (Decision (Eric,
	// 2026-10-21), Milestone 7 item 4).
	keepUntil := p.Expiry.Add(token.Leeway + time.Second).UnixMilli()
	err = a.core.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		return tx.PutAssertionJTI(ctx, p.Identity, p.JTI, keepUntil)
	})
	if errors.Is(err, store.ErrConflict) {
		return nil, core.E(bus.CodeUnauthenticated, "assertion replayed")
	}
	if err != nil {
		return nil, core.FromStore(err)
	}
	raw, _, err := iss.Mint(p.Identity)
	if err != nil {
		return nil, bus.Internal(err)
	}
	return noStoreToken{gen.Token200JSONResponse{
		AccessToken: raw,
		TokenType:   "Bearer",
		ExpiresIn:   int64(iss.Lifetime().Seconds()),
	}}, nil
}

// Jwks serves the own issuer's public key set.
func (a *api) Jwks(context.Context, gen.JwksRequestObject) (gen.JwksResponseObject, error) {
	if a.opts.Issuer == nil {
		return nil, issuerUnavailable()
	}
	set := a.opts.Issuer.JWKS()
	out := gen.JWKS{Keys: make([]gen.JWK, 0, len(set.Keys))}
	for _, k := range set.Keys {
		b, err := k.MarshalJSON()
		if err != nil {
			return nil, bus.Internal(err)
		}
		var m gen.JWK
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, bus.Internal(err)
		}
		out.Keys = append(out.Keys, m)
	}
	return gen.Jwks200JSONResponse(out), nil
}

// Register binds a public key to an identity under the configured mode
// (Decisions 20, 58, 72). An unset mode is closed, never open.
func (a *api) Register(ctx context.Context, req gen.RegisterRequestObject) (gen.RegisterResponseObject, error) {
	if req.Body == nil {
		return nil, core.E(bus.CodeValidation, "request body is required")
	}
	raw, err := json.Marshal(req.Body.PublicKeyJwk)
	if err != nil {
		return nil, core.E(bus.CodeValidation, "public_key_jwk: %s", err)
	}
	var jwk jose.JSONWebKey
	if err := jwk.UnmarshalJSON(raw); err != nil {
		return nil, core.E(bus.CodeValidation, "public_key_jwk: %s", err)
	}
	pub, _, _, err := keys.FromJWK(jwk)
	if err != nil {
		return nil, core.E(bus.CodeValidation, "public_key_jwk: %s", err)
	}
	// Anything but open and enrollment is closed: "" and an unrecognized
	// value never register (and never reach the service's panic guard).
	mode := a.opts.Registration
	if mode != identities.ModeOpen && mode != identities.ModeEnrollment {
		mode = identities.ModeClosed
	}
	in := identities.RegisterInput{Identity: req.Body.Identity, Key: pub}
	if req.Body.EnrollmentToken != nil {
		in.EnrollmentToken = *req.Body.EnrollmentToken
	}
	res, err := identities.Service{Core: a.core}.Register(ctx, mode, in)
	if err != nil {
		return nil, err
	}
	return gen.Register201JSONResponse{
		Identity:  res.Identity,
		Kid:       res.KID,
		Algorithm: gen.RegistrationAlgorithm(res.Algorithm),
		Created:   res.Created,
	}, nil
}
