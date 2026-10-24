package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/ericfitz/agentbus2/internal/auth/token"
)

// Bearer authenticates a request by its Authorization: Bearer token (Decisions
// 57, 70): a token the own Issuer minted. A provider token is never accepted
// here (Decision 10); it is exchanged at the token endpoint. Bearer attaches
// the identity the token names and judges nothing about the identity's
// registration: the core answers not_registered (Milestone 5 choice 5).
type Bearer struct {
	Verifier token.Verifier
	// Now is the clock for own bearers; nil keeps Verifier.Now.
	Now func() time.Time
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
	id, err := v.Verify(r.Context(), raw)
	if err != nil {
		return "", fmt.Errorf("invalid bearer token: %s", verifyReason(err))
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
// default is malformed.
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
	}
	return "malformed"
}
