package store

import "context"

// RefList names a reference list (federated identity spec 6.2).
type RefList string

const (
	// ListUsers admits a human to the server.
	ListUsers RefList = "users"
	// ListAdmins grants a human (or an agent) the admin flag.
	ListAdmins RefList = "admins"
)

// RefKind says what a Reference value matches.
type RefKind string

const (
	RefEmail    RefKind = "email"
	RefGroup    RefKind = "group"
	RefSubject  RefKind = "subject"
	RefIdentity RefKind = "identity"
)

// Reference is one entry of a reference list. The claim kinds (RefEmail,
// RefGroup, RefSubject) need the provider's Issuer; RefIdentity names an
// identity and has none.
type Reference struct {
	List      RefList
	Kind      RefKind
	Issuer    string
	Value     string
	CreatedAt int64
}

// ValidRefList reports whether l is one of the two lists.
func ValidRefList(l RefList) bool { return l == ListUsers || l == ListAdmins }

// ValidRefKind reports whether k is one of the four kinds.
func ValidRefKind(k RefKind) bool {
	return k == RefEmail || k == RefGroup || k == RefSubject || k == RefIdentity
}

// References stores the users and admins lists.
type References interface {
	// AddReference answers false for an existing row. ErrInvalid: an unknown
	// list or kind, RefIdentity with an issuer, a claim kind without one, an
	// empty value, or text failing TextRule.
	AddReference(ctx context.Context, r Reference) (bool, error)
	// RemoveReference answers false for no row; the same ErrInvalid cases.
	RemoveReference(ctx context.Context, r Reference) (bool, error)
	// ListReferences orders by kind, issuer, value in byte order; ErrInvalid
	// on an unknown list.
	ListReferences(ctx context.Context, list RefList) ([]Reference, error)
}
