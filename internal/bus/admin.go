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
}
