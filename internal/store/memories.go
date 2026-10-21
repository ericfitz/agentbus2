package store

import "context"

// MemoryRef names a memory's live revision.
type MemoryRef struct{ MemoryID, Seq int64 }

type Memories interface {
	LiveMemory(ctx context.Context, memoryID int64) (Message, error)        // tombstone = false; ErrNotFound
	MemoryRevisions(ctx context.Context, memoryID int64) ([]Message, error) // by revision
	SetMemory(ctx context.Context, seq, memoryID, revision int64) error
	TouchMemoryAccess(ctx context.Context, ids []int64, at int64) error // upsert
	DeleteMemoryAccess(ctx context.Context, memoryID int64) error
	// ExpiredMemories: live memories whose access row is older (v1
	// memoryExpiryChunk).
	ExpiredMemories(ctx context.Context, accessedBefore int64, limit int) ([]MemoryRef, error)
}

// Tasks reads task lists; tasks are memory rows on a tasks channel (plan
// choice 8, Decision 90).
type Tasks interface {
	// LiveMemories: live memory rows on the channel, ascending seq (v1
	// loadTasks).
	LiveMemories(ctx context.Context, channelID int64) ([]Message, error)
	// ChannelLastActivity: max of created_at and tombstone_at over the
	// channel's messages and the channel's created_at (v1 taskLastActivitySQL).
	ChannelLastActivity(ctx context.Context, channelID int64) (int64, error)
}
