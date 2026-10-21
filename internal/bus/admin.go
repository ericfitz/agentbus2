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

// Admin is the administrator seam. DeleteChannel and RenameChannel repeat
// the Messaging operations with identical signatures (Decision 89: the same
// operations, in the admin section).
type Admin interface {
	Status(ctx context.Context) (Status, error)
	DeleteChannel(ctx context.Context, in DeleteChannelInput) error
	RenameChannel(ctx context.Context, in RenameChannelInput) error
	Reset(ctx context.Context, in ResetInput) error
	ResetCredential(ctx context.Context, in ResetCredentialInput) (ResetCredentialResult, error)
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
