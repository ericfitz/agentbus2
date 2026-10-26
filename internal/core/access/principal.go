package access

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Principal is who asks: the identity row as the Store holds it. Scope is ""
// for an unscoped identity (every human is unscoped).
type Principal struct {
	Name  string
	Kind  store.IdentityKind
	Admin bool
	Scope string
}

// Anonymous is the unauthenticated caller (the health probe tier). It holds
// nothing and is denied every action.
var Anonymous = Principal{}

// Resolve builds the Principal for a registered identity. "", the reserved
// names everyone, admins, humans and slack-bridge, and an unknown name are
// not_registered.
func Resolve(ctx context.Context, tx store.Tx, name string) (Principal, error) {
	if name == "" || store.ReservedIdentity(name) {
		return Principal{}, notRegistered(name)
	}
	id, err := tx.Identity(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return Principal{}, notRegistered(name)
	}
	if err != nil {
		return Principal{}, fromStore(err)
	}
	return principalOf(id), nil
}

func principalOf(id store.Identity) Principal {
	return Principal{Name: id.Name, Kind: id.Kind, Admin: id.Admin, Scope: id.Scope}
}

func notRegistered(name string) error {
	return bus.Errorf(bus.CodeNotRegistered, false, "%q is not a registered identity", name)
}

// fromStore maps a Store error to a bus.Error (the mapping core.FromStore
// applies; this package may not import internal/core).
func fromStore(err error) error {
	if err == nil {
		return nil
	}
	var be *bus.Error
	switch {
	case errors.As(err, &be):
		return err
	case errors.Is(err, store.ErrNotFound):
		return bus.Errorf(bus.CodeNotFound, false, "%s", err.Error())
	case errors.Is(err, store.ErrRetryable):
		return bus.Errorf(bus.CodeUnavailable, true, "%s", err.Error())
	}
	return bus.Internal(err)
}
