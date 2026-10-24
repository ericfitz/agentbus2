package identities

import (
	"context"
	"errors"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/channels"
	"github.com/ericfitz/agentbus2/internal/core/scopes"
	"github.com/ericfitz/agentbus2/internal/store"
)

// agentTokenLifetime is how long the token bound to a created agent identity
// lives (plan choice of the Slack bridge, 24 hours, one use).
const agentTokenLifetime = 24 * time.Hour

// CreateAgentIdentity is the host command's call: CreateAgentIdentityAs
// without an administrator, since a host command holds no credential and
// reaches the Store directly.
func (s Service) CreateAgentIdentity(ctx context.Context, in bus.CreateAgentIdentityInput) (bus.AgentIdentity, error) {
	return s.createAgentIdentity(ctx, "", in)
}

// CreateAgentIdentityAs creates a keyless agent identity, optionally in a
// scope, and a one-time enrollment token bound to its name (Decision 17).
// The agent registers its own key with the token, so the private key never
// leaves its host. Administrator only.
//
// Rules: scope "" makes an unscoped agent with the general subscription; a
// scope must be open, and the agent gets no general subscription (Decision
// 19). An existing keyless agent row of the same scope answers Created false
// with a fresh token, the earlier unused tokens bound to the name revoked in
// the same transaction. A row with a key, in another scope (or unscoped vs
// scoped), or a human is conflict.
//
// There is no idempotency key: the token is never stored in clear, so a
// receipt could not replay it; the revoke-and-reissue above is the retry.
//
// State (Definition of Done D): a second run changes nothing but the token;
// two overlapping runs end with exactly one live bound token (the Store
// serializes the transactions and each revokes before it inserts); a failed
// run leaves nothing, since the row, the revocation and the token are one
// transaction.
func (s Service) CreateAgentIdentityAs(ctx context.Context, admin string, in bus.CreateAgentIdentityInput) (bus.AgentIdentity, error) {
	if admin == "" {
		return bus.AgentIdentity{}, core.E(bus.CodeNotRegistered, "%q is not a registered identity", admin)
	}
	return s.createAgentIdentity(ctx, admin, in)
}

func (s Service) createAgentIdentity(ctx context.Context, admin string, in bus.CreateAgentIdentityInput) (bus.AgentIdentity, error) {
	if store.ReservedIdentity(in.Name) {
		return bus.AgentIdentity{}, core.E(bus.CodeValidation, "identity name %q is reserved", in.Name)
	}
	if err := store.NameRule(in.Name); err != nil {
		return bus.AgentIdentity{}, core.E(bus.CodeValidation, "identity name %q: %s", in.Name, err)
	}
	if err := (channels.Service{Core: s.Core}).EnsureGeneral(ctx); err != nil {
		return bus.AgentIdentity{}, err
	}
	token, row, err := s.mintToken(in.Name, agentTokenLifetime, 1)
	if err != nil {
		return bus.AgentIdentity{}, err
	}
	var res bus.AgentIdentity
	for attempt := 1; ; attempt++ {
		err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
			res = bus.AgentIdentity{}
			if admin != "" {
				if _, err := s.RequireAdmin(ctx, tx, admin); err != nil {
					return err
				}
			}
			created, err := s.ensureAgent(ctx, tx, in)
			if err != nil {
				return err
			}
			if _, err := tx.DeleteEnrollmentTokensFor(ctx, in.Name); err != nil {
				return err
			}
			if err := tx.PutEnrollmentToken(ctx, row); err != nil {
				return err
			}
			res = bus.AgentIdentity{Identity: in.Name, Scope: in.Scope, Created: created, EnrollmentToken: token, ExpiresAt: row.ExpiresAt}
			return nil
		})
		if errors.Is(err, store.ErrConflict) && attempt < maxAttempts {
			continue
		}
		break
	}
	if err != nil {
		return bus.AgentIdentity{}, core.FromStore(err)
	}
	return res, nil
}

// ensureAgent creates the keyless agent row or checks the existing one fits.
func (s Service) ensureAgent(ctx context.Context, tx store.Tx, in bus.CreateAgentIdentityInput) (bool, error) {
	if in.Scope != "" {
		if _, err := scopes.RequireOpen(ctx, s.Core, tx, in.Scope); err != nil {
			return false, err
		}
	}
	id, err := tx.Identity(ctx, in.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		if in.Scope == "" {
			err = createWithGeneral(ctx, tx, in.Name, s.NowMs())
		} else {
			err = tx.CreateScopedIdentity(ctx, in.Name, in.Scope, s.NowMs())
		}
		return err == nil, err
	case err != nil:
		return false, err
	}
	if id.Kind == store.IdentityHuman {
		return false, core.E(bus.CodeConflict, "identity %q is a human; it takes no agent token", in.Name)
	}
	if id.Scope != in.Scope {
		return false, core.E(bus.CodeConflict, "identity %q exists in %s, not in %s", in.Name, scopeText(id.Scope), scopeText(in.Scope))
	}
	ks, err := tx.PublicKeys(ctx, in.Name)
	if err != nil {
		return false, err
	}
	if len(ks) > 0 {
		return false, core.E(bus.CodeConflict, "identity %q already has a credential", in.Name)
	}
	return false, nil
}

func scopeText(scope string) string {
	if scope == "" {
		return "no scope"
	}
	return "scope " + `"` + scope + `"`
}
