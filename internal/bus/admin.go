package bus

import "context"

// Status is the administrator's view: live sessions, every channel with its
// creator, server notices and the embedding backlog and rejections.
type Status struct {
	Sessions              []SessionInfo
	Channels              []ChannelInfo
	Notices               []Notice
	EmbeddingBacklog      int
	EmbeddingRejected     int
	EmbeddingRejectedLast string
	EmbeddingError        string
	EmbeddingErrorAt      int64
}

// Usage is the database size, the configured SQLite budget and the capacity
// notice. Evictable is false on Postgres, where no budget is enforced.
// Notice is the capacity notice text when one is set.
type Usage struct {
	Bytes, BudgetBytes int64
	Evictable          bool
	Notice             string
}

// Admin is the administrator seam. DeleteChannel and RenameChannel repeat
// the Messaging operations with identical signatures (Decision 89: the same
// operations, in the admin section).
type Admin interface {
	Status(ctx context.Context) (Status, error)
	Usage(ctx context.Context) (Usage, error)
	DeleteChannel(ctx context.Context, in DeleteChannelInput) error
	RenameChannel(ctx context.Context, in RenameChannelInput) error
	Reset(ctx context.Context, in ResetInput) error
	ResetCredential(ctx context.Context, in ResetCredentialInput) (ResetCredentialResult, error)
	CreateIdentityByEmail(ctx context.Context, in CreateIdentityByEmailInput) (CreateIdentityByEmailResult, error)
	CreateScope(ctx context.Context, in CreateScopeInput) (Scope, error)
	ListScopes(ctx context.Context) ([]Scope, error)
	GetScope(ctx context.Context, name string) (Scope, error)
	DoneScope(ctx context.Context, in DoneScopeInput) (Scope, error)
	CreateAgentIdentity(ctx context.Context, in CreateAgentIdentityInput) (AgentIdentity, error)
	// The bridge operations answer unavailable (not retryable) with BridgeNotConfigured when no
	// Slack bridge is wired (local mode has none).
	CreateBridge(ctx context.Context, in CreateBridgeInput) (BridgeCreated, error)
	ListBridges(ctx context.Context) ([]Bridge, error)
	GetBridge(ctx context.Context, channel string) (Bridge, error)
	RemoveBridge(ctx context.Context, in RemoveBridgeInput) error
}

// ResetScope selects what Reset removes (Milestone 5 choice 12).
type ResetScope string

// The reset scopes: data keeps identities and keys; everything removes them too.
const (
	ResetData       ResetScope = "data"
	ResetEverything ResetScope = "everything"
)

// ResetInput is Reset's input; an empty Scope means ResetData.
type ResetInput struct {
	Scope          ResetScope
	IdempotencyKey string
}

// ResetCredentialInput names the identity whose registered keys are removed.
type ResetCredentialInput struct{ Identity, IdempotencyKey string }

// ResetCredentialResult reports how many keys were removed.
type ResetCredentialResult struct {
	Identity    string
	KeysRemoved int
}

// CreateIdentityByEmailInput names the email a human identity is created for
// (spec 4.3); the core normalizes it.
type CreateIdentityByEmailInput struct{ Email, IdempotencyKey string }

// CreateIdentityByEmailResult is the normalized identity name and whether
// this call created it.
type CreateIdentityByEmailResult struct {
	Identity string
	Created  bool
}
