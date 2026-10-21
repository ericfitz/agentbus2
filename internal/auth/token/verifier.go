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

// Verifier routes a bearer to the own issuer or a configured external one by its iss.
type Verifier struct {
	Own      *Issuer // nil: no own issuer
	External []*External
	// Now is the clock for own bearers only (nil: time.Now). It does not reach
	// External: each External judges its claims and its JWKS cache by its own
	// Now, so a caller that injects a clock sets External.Now as well.
	Now func() time.Time
}

// Verify returns the identity of a valid bearer and whether an external issuer
// vouched for it. An iss that is empty or matches no issuer is ErrUnknownIssuer.
func (v Verifier) Verify(ctx context.Context, raw string) (string, bool, error) {
	iss, err := IssuerOf(raw)
	if err != nil {
		return "", false, err
	}
	if iss == "" {
		return "", false, ErrUnknownIssuer
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	if v.Own != nil && iss == v.Own.URL() {
		id, err := v.Own.Verify(raw, now())
		return id, false, err
	}
	for _, e := range v.External {
		if e != nil && e.Issuer == iss {
			id, err := e.Verify(ctx, raw)
			if err != nil {
				return "", false, err
			}
			return id, true, nil
		}
	}
	return "", false, ErrUnknownIssuer
}
