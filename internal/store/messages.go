package store

import (
	"context"

	"github.com/ericfitz/agentbus2/internal/search"
)

// Message is a stored message. Tombstoned rows keep their seq.
type Message struct {
	Seq, ChannelID         int64
	Sender, SessionID      string
	CreatedAt              int64
	Type, Subject, Content string
	ReplyTo                *int64
	Metadata               map[string]string
	MemoryID, Revision     *int64
	Tombstone              bool
	TombstoneAt            *int64
	Tags                   []string
}

// NewMessage is the input of AppendMessage; the Store assigns Seq.
type NewMessage struct {
	ChannelID              int64
	Sender, SessionID      string
	CreatedAt              int64
	Type, Subject, Content string
	ReplyTo                *int64
	Metadata               map[string]string
	MemoryID, Revision     *int64
	Tags                   []string
}

// SeqChannel names a message by seq and channel.
type SeqChannel struct{ Seq, ChannelID int64 }

// Embedding is a stored vector for a live memory (Decision 51).
type Embedding struct {
	Seq    int64
	Model  string
	Vector []byte
}

// EmbedFailure records a failed embedding attempt.
type EmbedFailure struct {
	Seq          int64
	Model, Error string
	FailedAt     int64
}

type Messages interface {
	// AppendMessage: Append only; NextSeq, insert, tags, dialect NotifyAppend.
	AppendMessage(ctx context.Context, m NewMessage) (int64, error)
	Message(ctx context.Context, seq int64) (Message, error)                                    // tombstoned rows too; ErrNotFound
	MessagesAfter(ctx context.Context, channelID, afterSeq int64, limit int) ([]Message, error) // live, ascending
	// MessagesInRange: live, fromSeq <= seq <= toSeq, every pattern matched
	// by some tag (v1 tagsFilter).
	MessagesInRange(ctx context.Context, channelID, fromSeq, toSeq int64, patterns []TagPattern, limit int) ([]Message, error)
	// SearchText: live, on channelIDs only; ErrInvalid on an empty query
	// (checked before the dialect is asked) or a term failing TextRule;
	// empty channelIDs: empty result, no query.
	SearchText(ctx context.Context, q search.Query, channelIDs []int64, limit int) ([]Message, error)
	// TombstoneMessage returns false when already tombstoned or missing.
	TombstoneMessage(ctx context.Context, seq, at int64) (bool, error)
	DeleteMessages(ctx context.Context, seqs []int64) error
	// ExpiredTombstones: tombstone and tombstone_at < before.
	ExpiredTombstones(ctx context.Context, before int64, limit int) ([]int64, error)
	// OrdinaryMessagesBefore: memory_id IS NULL and created_at < createdBefore,
	// ascending seq (v1 deleteOrdinaryChunk).
	OrdinaryMessagesBefore(ctx context.Context, createdBefore int64, limit int) ([]SeqChannel, error)
	// UnembeddedMemories: v1 unembeddedFrom: live memories on channels of kind
	// <> 'tasks' with no embedding and no failure for model. ErrInvalid on a
	// model failing TextRule, as for DeleteStaleModel and the puts.
	UnembeddedMemories(ctx context.Context, model string, limit int) ([]Message, error)
	// PutEmbedding and PutEmbedFailure replace; they act only when the message
	// is a live memory (v1 embed.go), else no-op.
	PutEmbedding(ctx context.Context, e Embedding) error
	PutEmbedFailure(ctx context.Context, f EmbedFailure) error
	DeleteEmbedding(ctx context.Context, seq int64) error
	// DeleteStaleModel deletes rows whose model <> model, in both tables.
	DeleteStaleModel(ctx context.Context, model string) error
	// Embeddings: live memories on channelIDs.
	Embeddings(ctx context.Context, model string, channelIDs []int64) ([]Embedding, error)
}
