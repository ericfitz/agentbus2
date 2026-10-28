package token

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// IssuerOf reads the iss claim of a compact JWT without verifying anything. It
// is for routing only; the issuer it names then verifies the whole token.
func IssuerOf(raw string) (string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", invalid("not a compact JWS")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", invalidCause("payload", err)
	}
	var c struct {
		Iss any `json:"iss"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return "", invalidCause("payload", err)
	}
	iss, _ := c.Iss.(string)
	return iss, nil
}

// Verifier verifies a bearer against the own issuer. A provider token is never
// a bearer (Decision 10): it is exchanged at the token endpoint for an own one.
type Verifier struct {
	Own *Issuer // nil: no own issuer
	// Now is the clock for own bearers (nil: time.Now).
	Now func() time.Time
}

// Verify returns the identity of a valid own bearer. An iss that is empty or
// not the own issuer's is ErrUnknownIssuer.
func (v Verifier) Verify(ctx context.Context, raw string) (string, error) {
	iss, err := IssuerOf(raw)
	if err != nil {
		return "", err
	}
	if iss == "" || v.Own == nil || iss != v.Own.URL() {
		return "", ErrUnknownIssuer
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	return v.Own.Verify(ctx, raw, now())
}
