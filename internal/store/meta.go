package store

import "context"

// Meta holds the sequence counter, leases and notices.
type Meta interface {
	// NextSeq allocates the next message sequence number (Decision 27).
	// Append only; ErrNotAppend otherwise.
	NextSeq(ctx context.Context) (int64, error)
	// MaxSeq returns seq_counter.v.
	MaxSeq(ctx context.Context) (int64, error)
	// TryLease takes or renews the named lease (v1 takeLease): it succeeds
	// when the lease is expired (expires_at < now) or already held by owner.
	TryLease(ctx context.Context, name, owner string, now, until int64) (bool, error)
	SetNotice(ctx context.Context, n Notice) error
	ClearNotice(ctx context.Context, kind string) error
	Notice(ctx context.Context, kind string) (Notice, bool, error)
}

// Notice is an operator-visible condition (capacity, embedding), keyed by Kind.
type Notice struct {
	Kind, Message string
	SetAt         int64
}
