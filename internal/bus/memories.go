package bus

import "context"

// EditMemoryInput writes a new revision of a memory. A nil pointer or slice
// keeps the live revision's value and an explicitly empty one clears it
// (post-build decision 10): Subject and Type "", Metadata an empty map, Tags an
// empty list. Content cannot be cleared: a pointer to "" is a validation
// error, since the memory is deleted instead. IfRevision 0 skips the check;
// otherwise the edit fails with CodeConflict unless the live revision is
// IfRevision.
type EditMemoryInput struct {
	ID             int64
	Subject        *string
	Content        *string
	Type           *string
	Metadata       *map[string]string
	Tags           []string
	IfRevision     int64
	IdempotencyKey string
}

// EditResult identifies the new revision and the one it replaced.
type EditResult struct {
	Seq      int64 `json:"seq"`
	MemoryID int64 `json:"memory_id"`
	Revision int64 `json:"revision"`
	Replaced int64 `json:"replaced"`
}

// DeleteMemoryInput tombstones a memory.
type DeleteMemoryInput struct {
	ID             int64
	IdempotencyKey string
}

// Memories is the memory seam. The getter is GetMemory, not Get: Bus embeds
// Memories and Tasks, and Go allows an overlapping method name in embedded
// interfaces only with an identical signature.
type Memories interface {
	GetMemory(ctx context.Context, id int64) (Message, error)
	Edit(ctx context.Context, in EditMemoryInput) (EditResult, error)
	Delete(ctx context.Context, in DeleteMemoryInput) error
	Revisions(ctx context.Context, id int64) ([]Message, error)
}
