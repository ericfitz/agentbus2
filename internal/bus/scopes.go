package bus

// ScopeState is the lifecycle state of a scope (Slack bridge spec 4.1).
type ScopeState string

// The scope states.
const (
	ScopeOpen    ScopeState = "open"
	ScopeDone    ScopeState = "done"
	ScopeRetired ScopeState = "retired"
)

// RetireMode says how a done scope is retired (Decision 7).
type RetireMode string

// The retire modes.
const (
	RetireWithBridge RetireMode = "with_bridge"
	RetireNormal     RetireMode = "normal"
)

// Scope is a named group of channels and agent identities retired as one
// unit. Channels and Identities are filled by Get only. Retire is "" until the
// scope is done.
type Scope struct {
	Name                            string
	State                           ScopeState
	Retire                          RetireMode
	CreatedAt, DoneAt, LastActivity int64
	Channels, Identities            []string
}

// CreateScopeInput creates a scope (administrator only).
type CreateScopeInput struct{ Name, IdempotencyKey string }

// DoneScopeInput says a scope is done and how it is retired (administrator
// only).
type DoneScopeInput struct {
	Name           string
	Retire         RetireMode
	IdempotencyKey string
}

// CreateAgentIdentityInput creates an agent identity, optionally in a scope
// (administrator only; Decision 17).
type CreateAgentIdentityInput struct{ Name, Scope string }

// AgentIdentity is the result of CreateAgentIdentity: the identity, its scope
// ("" when unscoped), whether this call created the row, and the one-time
// enrollment token bound to the name (24 hours, one use). The token is never
// stored in clear.
type AgentIdentity struct {
	Identity, Scope string
	Created         bool
	EnrollmentToken string
	ExpiresAt       int64
}
