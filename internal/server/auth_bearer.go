package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/token"
)

// Bearer authenticates a request by its Authorization: Bearer token (Decisions
// 57, 70): a token the own Issuer minted or one a configured External issuer
// signed. It attaches the identity the token names and judges nothing about
// the identity's registration: the core answers not_registered (Milestone 5
// choice 5).
type Bearer struct {
	Verifier token.Verifier
	// Now is the clock for own bearers; nil keeps Verifier.Now.
	Now func() time.Time
	// AutoCreate, when non-nil, is called with the identity of a bearer an
	// External issuer verified (never one the own Issuer verified, so a
	// deleted identity's own token stays 403 not_registered). Run sets it to
	// identities.Service.CreateIdentity only in open registration mode
	// (Decision (Eric, 2026-10-21), item 5). It is idempotent; its error is
	// logged and the request goes on to the core, which answers
	// not_registered.
	AutoCreate func(ctx context.Context, name string) error
	// Logger receives AutoCreate errors; nil discards them.
	Logger *slog.Logger
}

// Authenticate implements Authenticator. No header gives "" and nil; a header
// that is not "Bearer <token>" or a token that fails verification is an error
// whose text carries one reason and never the token.
func (b Bearer) Authenticate(r *http.Request) (string, error) {
	h := r.Header.Values("Authorization")
	if len(h) == 0 {
		return "", nil
	}
	if len(h) > 1 {
		return "", errors.New("malformed Authorization header")
	}
	raw, ok := bearerToken(h[0])
	if !ok {
		return "", errors.New("malformed Authorization header")
	}
	v := b.Verifier
	if b.Now != nil {
		v.Now = b.Now
	}
	id, external, err := v.Verify(r.Context(), raw)
	if err != nil {
		return "", fmt.Errorf("invalid bearer token: %s", verifyReason(err))
	}
	if external && b.AutoCreate != nil {
		if err := b.AutoCreate(r.Context(), id); err != nil {
			log := b.Logger
			if log == nil {
				log = slog.New(slog.DiscardHandler)
			}
			log.Error("auto-creating the identity of an external subject", "identity", id, "err", err)
		}
	}
	return id, nil
}

// bearerToken extracts the token of "Bearer <token>": the scheme is matched
// case-insensitively, followed by exactly one space and a token with no
// whitespace.
func bearerToken(h string) (string, bool) {
	const scheme = "bearer "
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	t := h[len(scheme):]
	if strings.ContainsAny(t, " \t\r\n") {
		return "", false
	}
	return t, true
}

// verifyReason maps a verification error to one of the fixed reasons; the
// default is malformed. A JWKS fetch failure of an external issuer also lands
// on malformed: the closed reason list has no better word, and the request is
// refused either way.
func verifyReason(err error) string {
	switch {
	case errors.Is(err, token.ErrUnknownIssuer), errors.Is(err, jwt.ErrInvalidIssuer):
		return "unknown issuer"
	case errors.Is(err, token.ErrUnknownKey):
		return "unknown key"
	case errors.Is(err, jwt.ErrExpired):
		return "expired"
	case errors.Is(err, jwt.ErrInvalidAudience):
		return "wrong audience"
	case errors.Is(err, jose.ErrCryptoFailure):
		return "bad signature"
	case strings.Contains(err.Error(), "is not mapped"):
		return "unmapped subject"
	}
	return "malformed"
}
