package store

import "context"

// ScopeState is the lifecycle state of a scope (Slack bridge spec 4).
type ScopeState string

const (
	ScopeOpen    ScopeState = "open"
	ScopeDone    ScopeState = "done"
	ScopeRetired ScopeState = "retired"
)

// RetireMode says how a done scope is retired.
type RetireMode string

const (
	RetireWithBridge RetireMode = "with_bridge"
	RetireNormal     RetireMode = "normal"
)

// ValidRetireMode reports whether m is exactly with_bridge or normal.
func ValidRetireMode(m RetireMode) bool { return m == RetireWithBridge || m == RetireNormal }

// Scope is a named group of channels and identities that is retired as one
// unit. Retire is "" until MarkScopeDone; DoneAt and RetiredAt are 0 until
// then.
type Scope struct {
	Name                                       string
	State                                      ScopeState
	Retire                                     RetireMode
	CreatedAt, DoneAt, RetiredAt, LastActivity int64
}

// Scopes is the scope registry. A scope name is checked with NameRule and
// shares no namespace with channel or identity names.
type Scopes interface {
	// CreateScope: ErrInvalid (NameRule); ErrConflict when the name exists in
	// any state. LastActivity = now.
	CreateScope(ctx context.Context, name string, now int64) error
	Scope(ctx context.Context, name string) (Scope, error) // ErrNotFound
	ListScopes(ctx context.Context) ([]Scope, error)       // every state, by name in byte order
	// MarkScopeDone: ErrInvalid (unknown mode); ErrNotFound; ErrConflict
	// unless the scope is open.
	MarkScopeDone(ctx context.Context, name string, mode RetireMode, now int64) error
	// MarkScopeRetired: ErrNotFound; nil when already retired.
	MarkScopeRetired(ctx context.Context, name string, now int64) error
	// DeleteRetiredScopes removes retired scopes with retired_at <
	// retiredBefore that no channel or identity references, and returns how
	// many it removed.
	DeleteRetiredScopes(ctx context.Context, retiredBefore int64) (int, error)
	// CreateScopedIdentity is CreateIdentity plus the scope; ErrNotFound for
	// an unknown scope; ErrInvalid ("an administrator cannot be scoped", "a
	// human identity cannot be scoped") for a name whose row is an
	// administrator or a human, or that is on the admins list (spec 4.3, 4.5).
	CreateScopedIdentity(ctx context.Context, name, scope string, now int64) error
	// ScopeMembers lists the scope's channels and identities, each by name in
	// byte order.
	ScopeMembers(ctx context.Context, scope string) ([]Channel, []Identity, error)
	// RetireIdentity, in one transaction, deletes the name's channel_acl rows
	// on every channel, its bound enrollment tokens and then the identity row
	// (keys, subscriptions, tag subscriptions, sessions with their cursors and
	// slack_users cascade), and records the name in retired_identities. It
	// reports false, nil when the row is already gone (also when an overlapping run removed it first), ErrInvalid for a reserved name. Messages stay.
	RetireIdentity(ctx context.Context, name string, now int64) (bool, error)
	// IdentityRetired reports whether the name is in retired_identities.
	IdentityRetired(ctx context.Context, name string) (bool, error)
}
