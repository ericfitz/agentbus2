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
	// identity_keys, human_bindings, access_refs (the users and admins lists),
	// enrollment_tokens, scopes, retired_identities, bridges, bridge_messages,
	// bridge_dropped and slack_users (a bridge whose channel it removed is
	// cleaned by the Slack bridge runner). Write transactions only
	// (ErrReadOnly in Read). Safe to repeat.
	ResetData(ctx context.Context) error
	// ResetRegistry deletes every identities row except keep and slack-bridge (identity_keys
	// cascades; keep "" deletes all; human_bindings cascade with their
	// identities), every enrollment_tokens row, every users reference and
	// every admins reference except {admins, identity, "", keep}; it keeps
	// signing_keys and everything else ResetData keeps (the core calls
	// ResetData first). The Store adds no admins row for keep. ErrInvalid when keep fails TextRule. Write transactions only.
	ResetRegistry(ctx context.Context, keep string) error
}

// Notice is an operator-visible condition (capacity, embedding), keyed by Kind.
type Notice struct {
	Kind, Message string
	SetAt         int64
}
