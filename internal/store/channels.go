package store

import "context"

// Kind is the stored channel kind (Decision 90).
type Kind string

const (
	KindOrdinary Kind = "ordinary"
	KindMemory   Kind = "memory"
	KindTasks    Kind = "tasks"
)

// Channel is a stored channel. Creator "" means none (Decision 92); it is
// stored as ” and never NULL.
type Channel struct {
	ID                                      int64
	Name                                    string
	Kind                                    Kind
	Creator                                 string
	Scope                                   string // "" when unscoped
	CreatedSeq, EvictedBeforeSeq, CreatedAt int64
	LastReadAt                              int64 // the latest TouchChannelRead; 0 when never read
}

// ChannelCreate is the input of CreateChannel. ACL nil means "without an
// ACL": the Store stores DefaultACL(Creator). A non-nil ACL, even an empty
// one, is stored as given (plan choice 9); the implicit entries still apply.
type ChannelCreate struct {
	Name                  string
	Kind                  Kind
	Creator               string
	Scope                 string // "" is unscoped (stored NULL); ErrNotFound for an unknown scope
	ACL                   []ACLEntry
	CreatedSeq, CreatedAt int64
}

// ACLRow is one stored channel_acl row with its channel id (the result of
// ACLRowsFor).
type ACLRow struct {
	ChannelID          int64
	Identity           string
	Read, Write, Admin bool
}

type Channels interface {
	// CreateChannel: ErrInvalid (name, kind, ACL), ErrConflict (name).
	CreateChannel(ctx context.Context, c ChannelCreate) (Channel, error)
	Channel(ctx context.Context, id int64) (Channel, error)
	ChannelByName(ctx context.Context, name string) (Channel, error)
	ListChannels(ctx context.Context) ([]Channel, error)               // by name, in byte order
	ListChannelsByKind(ctx context.Context, k Kind) ([]Channel, error) // by name, in byte order; ErrInvalid on an unknown kind
	// RenameChannel: ErrInvalid, ErrConflict, ErrNotFound (Decision 89).
	RenameChannel(ctx context.Context, id int64, name string) error
	// DeleteChannel cascades to ACL rows, subscriptions, cursors, messages.
	DeleteChannel(ctx context.Context, id int64) error
	// EmptyChannelsBefore lists the ordinary channels created before
	// createdBefore that have no message row (tombstones count) and no
	// counting subscription, by name in byte order. A subscription counts only
	// when its identity is not slack-bridge and holds a session that is not
	// closed and whose lease_expires_at >= resumableAfter (Decision 20); tag
	// subscriptions do not count. A channel with a bridges row whose state is
	// not archived is never offered. It does not know which channel the server
	// protects: the caller skips `general` (Decision (Eric, 2026-10-21),
	// Milestone 3 item 8; v1 reapEmptyChannels).
	EmptyChannelsBefore(ctx context.Context, createdBefore, resumableAfter int64) ([]Channel, error)
	// DeleteChannelIfEmpty deletes the channel when it still satisfies
	// EmptyChannelsBefore's condition, in one statement, and reports whether
	// it did. A channel that is gone, not ordinary, too young, or no longer
	// empty is left alone and is not an error. Cascades like DeleteChannel.
	DeleteChannelIfEmpty(ctx context.Context, id, createdBefore, resumableAfter int64) (bool, error)
	// TouchChannelRead sets last_read_at to at when at is greater; a channel
	// that is gone or already read later changes nothing and is not an error.
	TouchChannelRead(ctx context.Context, id, at int64) error
	// SetEvictedBefore sets the mark to max(current, seq).
	SetEvictedBefore(ctx context.Context, id int64, seq int64) error
	ACL(ctx context.Context, id int64) ([]ACLEntry, error) // insertion order
	// SetACL validates all entries, then replaces all, in one transaction.
	SetACL(ctx context.Context, id int64, entries []ACLEntry) error
	// ACLRowsFor returns the stored channel_acl rows whose identity is one of
	// names, ordered by (channel_id, identity). It is a fact query: no
	// permission filter, no administrator join, no binding of an unregistered
	// or malformed name (such a name matches nothing). The rule over these
	// rows is internal/core/access.
	ACLRowsFor(ctx context.Context, names ...string) ([]ACLRow, error)
}
