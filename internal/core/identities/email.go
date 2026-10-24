package identities

import (
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// NormalizeEmail is the spec 4.2 rule: trim, lowercase the whole address; the
// result passes store.NameRule, is not everyone or admins and has one '@' with
// a non-empty part on each side. The error is validation.
func NormalizeEmail(raw string) (string, error) {
	e := strings.ToLower(strings.TrimSpace(raw))
	if err := store.NameRule(e); err != nil {
		return "", core.E(bus.CodeValidation, "email %q: %v", raw, err)
	}
	if e == store.Everyone || e == store.Admins {
		return "", core.E(bus.CodeValidation, "email %q is a reserved name", raw)
	}
	local, domain, ok := strings.Cut(e, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", core.E(bus.CodeValidation, "email %q must contain one '@' with text on each side", raw)
	}
	return e, nil
}

// HumanClaims are the verified claims of a signed-in human. IssuerName is the
// configured alias, Issuer the iss URL, Email already normalized.
type HumanClaims struct {
	IssuerName, Issuer, Email string
	Groups                    []string
	Subject                   string
}
