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
	// ResetData deletes every row of notices, messages (explicitly and first,
	// so SQLite's messages_ad trigger keeps messages_fts in step), channels
	// (channel_acl, subscriptions, session_cursors, message_tags, embeddings
	// and embed_failures cascade), subscriptions, tag_subscriptions
	// (tag_subscription_tags cascades), sessions, memory_access and receipts.
	// It keeps schema_meta, seq_counter, leases, signing_keys, identities,
	// identity_keys and enrollment_tokens. Write transactions only
	// (ErrReadOnly in Read). Safe to repeat.
	ResetData(ctx context.Context) error
	// ResetRegistry deletes every identities row except keep (identity_keys
	// cascades; keep "" deletes all) and every enrollment_tokens row; it keeps
	// signing_keys and everything ResetData keeps (the core calls ResetData
	// first). ErrInvalid when keep fails TextRule. Write transactions only.
	ResetRegistry(ctx context.Context, keep string) error
}

// Notice is an operator-visible condition (capacity, embedding), keyed by Kind.
type Notice struct {
	Kind, Message string
	SetAt         int64
}
