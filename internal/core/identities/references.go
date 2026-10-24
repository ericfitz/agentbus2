package identities

import (
	"context"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/store"
)

// ParseReference builds a Reference from host-command style inputs: list is
// users or admins; exactly one of email, group, subject, identity is set; a
// claim kind needs issuer, an identity reference refuses it; an email is
// normalized. The error is validation.
func ParseReference(list, issuer, email, group, subject, identity string) (store.Reference, error) {
	r := store.Reference{List: store.RefList(list)}
	if !store.ValidRefList(r.List) {
		return r, core.E(bus.CodeValidation, "unknown list %q: want %q or %q", list, store.ListUsers, store.ListAdmins)
	}
	set := 0
	for _, kv := range []struct {
		kind  store.RefKind
		value string
	}{{store.RefEmail, email}, {store.RefGroup, group}, {store.RefSubject, subject}, {store.RefIdentity, identity}} {
		if kv.value != "" {
			set++
			r.Kind, r.Value = kv.kind, kv.value
		}
	}
	if set != 1 {
		return r, core.E(bus.CodeValidation, "exactly one of email, group, subject and identity is required")
	}
	if r.Kind == store.RefIdentity {
		if issuer != "" {
			return r, core.E(bus.CodeValidation, "an identity reference takes no issuer")
		}
		if err := store.NameRule(r.Value); err != nil {
			return r, core.E(bus.CodeValidation, "identity %q: %v", r.Value, err)
		}
		return r, nil
	}
	if issuer == "" {
		return r, core.E(bus.CodeValidation, "a %s reference needs an issuer", r.Kind)
	}
	r.Issuer = issuer
	if r.Kind == store.RefEmail {
		v, err := NormalizeEmail(r.Value)
		if err != nil {
			return r, err
		}
		r.Value = v
	}
	return r, nil
}

// MatchReferences reports whether any reference matches: an identity
// reference by name; a claim reference only when its issuer is c.IssuerName,
// against the email, the group membership or the subject. Pure.
func MatchReferences(refs []store.Reference, identity string, c HumanClaims) bool {
	for _, r := range refs {
		switch r.Kind {
		case store.RefIdentity:
			if identity != "" && r.Value == identity {
				return true
			}
		case store.RefEmail:
			if r.Issuer == c.IssuerName && c.Email != "" && r.Value == c.Email {
				return true
			}
		case store.RefGroup:
			if r.Issuer == c.IssuerName && slices.Contains(c.Groups, r.Value) {
				return true
			}
		case store.RefSubject:
			if r.Issuer == c.IssuerName && c.Subject != "" && r.Value == c.Subject {
				return true
			}
		}
	}
	return false
}

// AddReference adds r; false means it was present. A claim kind needs
// r.Issuer among issuers. An identity reference needs the identity to exist,
// and on the admins list sets its flag in the same transaction.
func (s Service) AddReference(ctx context.Context, r store.Reference, issuers []string) (bool, error) {
	if err := checkReference(r); err != nil {
		return false, err
	}
	if r.Kind != store.RefIdentity && !slices.Contains(issuers, r.Issuer) {
		return false, core.E(bus.CodeValidation, "unknown issuer %q; configured: %s", r.Issuer, configured(issuers))
	}
	var added bool
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		added, err = addReference(ctx, tx, r)
		return err
	})
	return added, core.FromStore(err)
}

func addReference(ctx context.Context, tx store.Tx, r store.Reference) (bool, error) {
	if r.Kind == store.RefIdentity {
		if err := requireRegistered(ctx, tx, r.Value); err != nil {
			return false, err
		}
	}
	added, err := tx.AddReference(ctx, r)
	if err != nil {
		return false, err
	}
	if r.List == store.ListAdmins && r.Kind == store.RefIdentity {
		if err := tx.SetAdmin(ctx, r.Value, true); err != nil {
			return false, err
		}
	}
	return added, nil
}

// RemoveReference removes r; false means it was absent. There is no issuer
// check, so the entries of a removed issuer can be cleaned up. Removing an
// existing admins identity reference clears that identity's flag.
func (s Service) RemoveReference(ctx context.Context, r store.Reference) (bool, error) {
	if err := checkReference(r); err != nil {
		return false, err
	}
	var removed bool
	err := s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		removed, err = removeReference(ctx, tx, r)
		return err
	})
	return removed, core.FromStore(err)
}

func removeReference(ctx context.Context, tx store.Tx, r store.Reference) (bool, error) {
	removed, err := tx.RemoveReference(ctx, r)
	if err != nil {
		return false, err
	}
	if removed && r.List == store.ListAdmins && r.Kind == store.RefIdentity {
		if err := tx.SetAdmin(ctx, r.Value, false); err != nil {
			return false, err
		}
	}
	return removed, nil
}

// ListReferences answers the entries of list.
func (s Service) ListReferences(ctx context.Context, list store.RefList) ([]store.Reference, error) {
	if !store.ValidRefList(list) {
		return nil, core.E(bus.CodeValidation, "unknown list %q", list)
	}
	var refs []store.Reference
	err := s.Store.Read(ctx, func(ctx context.Context, tx store.Tx) error {
		var err error
		refs, err = tx.ListReferences(ctx, list)
		return err
	})
	return refs, core.FromStore(err)
}

// checkReference refuses a reference the parser would not build.
func checkReference(r store.Reference) error {
	if !store.ValidRefList(r.List) {
		return core.E(bus.CodeValidation, "unknown list %q", r.List)
	}
	if !store.ValidRefKind(r.Kind) {
		return core.E(bus.CodeValidation, "unknown reference kind %q", r.Kind)
	}
	if r.Value == "" {
		return core.E(bus.CodeValidation, "a reference needs a value")
	}
	if (r.Kind == store.RefIdentity) != (r.Issuer == "") {
		return core.E(bus.CodeValidation, "a claim reference needs an issuer and an identity reference takes none")
	}
	return nil
}

func configured(issuers []string) string {
	if len(issuers) == 0 {
		return "none"
	}
	return strings.Join(issuers, ", ")
}
