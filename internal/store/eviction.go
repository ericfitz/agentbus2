package store

import "context"

// BridgeMessageRef names a bridge_messages row by key; Bytes is the row's
// size_bytes.
type BridgeMessageRef struct{ BridgeID, Seq, Bytes int64 }

// TokenRef names an enrollment token; Bytes is the row's size_bytes.
type TokenRef struct {
	ID    string
	Bytes int64
}

// SizedMemory names a memory by id; Bytes is what PurgeMemory frees: every
// messages row of the memory (with the tag, embedding and embed-failure rows
// they cascade to) and its memory_access row.
type SizedMemory struct{ MemoryID, Bytes int64 }

// EvictionCutoffs are the four cutoffs EvictionLeft tests, one per kind of
// eviction candidate (all in milliseconds).
type EvictionCutoffs struct{ TombstoneBefore, TokenExpiredBefore, IdleBefore, MessageBefore int64 }

// EvictionLeft says which kinds of candidate remain: Useless (a bridge map row
// whose message is gone), Expired (an expired tombstone or enrollment token),
// Idle (an idle task list or memory) and Ordinary (an ordinary message older
// than the cutoff).
type EvictionLeft struct{ Useless, Expired, Idle, Ordinary bool }

// Early reports whether a candidate of the first two kinds remains.
func (l EvictionLeft) Early() bool { return l.Useless || l.Expired }

// Late reports whether a candidate of the last two kinds remains.
func (l EvictionLeft) Late() bool { return l.Idle || l.Ordinary }

// Eviction is what the budget sweep reads and deletes, beyond ExpiredTombstones
// and OrdinaryMessagesBefore (Messages). Every Bytes is the sum of the rows'
// size_bytes the delete frees, cascades included. Listings take a positive
// limit (ErrInvalid otherwise); the deletes need a write transaction.
type Eviction interface {
	// OrphanedBridgeMessages lists bridge_messages rows whose seq is no longer
	// a message, by (bridge_id, seq).
	OrphanedBridgeMessages(ctx context.Context, limit int) ([]BridgeMessageRef, error)
	// DeleteBridgeMessages deletes the rows named, only while still orphaned.
	DeleteBridgeMessages(ctx context.Context, refs []BridgeMessageRef) error
	// ExpiredEnrollmentTokens lists tokens with expires_at < now, by
	// (expires_at, id).
	ExpiredEnrollmentTokens(ctx context.Context, now int64, limit int) ([]TokenRef, error)
	DeleteEnrollmentTokens(ctx context.Context, ids []string) error
	// IdleTaskChannels lists the task lists (kind tasks) with no bridge that is
	// not archived and whose latest activity (the greatest of created_at,
	// last_read_at, and the created_at and tombstone_at of its messages) is
	// below idleBefore, oldest activity first, then id.
	IdleTaskChannels(ctx context.Context, idleBefore int64, limit int) ([]Channel, error)
	// ChannelBytes is the channel row plus every channel_acl, subscriptions
	// and session_cursors row and every message (tags, embeddings and failures
	// included) of the channel: what DeleteChannel frees. ErrNotFound for an
	// unknown id.
	ChannelBytes(ctx context.Context, channelID int64) (int64, error)
	// DeleteIdleTaskChannel deletes the channel in one conditional statement
	// that re-checks the IdleTaskChannels condition, and reports whether it did
	// (as DeleteChannelIfEmpty). An unknown id is false, not an error.
	DeleteIdleTaskChannel(ctx context.Context, channelID, idleBefore int64) (bool, error)
	// IdleMemories lists live memories (tombstone = FALSE, memory_id set) on
	// channels of any kind but tasks whose latest activity (the greatest of the
	// live row's created_at and its memory_access.accessed_at, 0 when absent)
	// is below idleBefore, oldest activity first, then memory id.
	IdleMemories(ctx context.Context, idleBefore int64, limit int) ([]SizedMemory, error)
	// PurgeMemory deletes every messages row of the memory and its
	// memory_access row (no foreign key joins them).
	PurgeMemory(ctx context.Context, memoryID int64) error
	// EvictionLeft answers each flag with the matching listing at limit 1.
	EvictionLeft(ctx context.Context, c EvictionCutoffs) (EvictionLeft, error)
}
