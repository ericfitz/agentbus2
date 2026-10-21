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
	CreatedSeq, EvictedBeforeSeq, CreatedAt int64
}

// ChannelCreate is the input of CreateChannel. ACL nil means "without an
// ACL": the Store stores DefaultACL(Creator). A non-nil ACL, even an empty
// one, is stored as given (plan choice 9); the implicit entries still apply.
type ChannelCreate struct {
	Name                  string
	Kind                  Kind
	Creator               string
	ACL                   []ACLEntry
	CreatedSeq, CreatedAt int64
}

type Channels interface {
	// CreateChannel: ErrInvalid (name, kind, ACL), ErrConflict (name).
	CreateChannel(ctx context.Context, c ChannelCreate) (Channel, error)
	Channel(ctx context.Context, id int64) (Channel, error)
	ChannelByName(ctx context.Context, name string) (Channel, error)
	ListChannels(ctx context.Context) ([]Channel, error)               // by name, in byte order
	ListChannelsByKind(ctx context.Context, k Kind) ([]Channel, error) // by name, in byte order
	// RenameChannel: ErrInvalid, ErrConflict, ErrNotFound (Decision 89).
	RenameChannel(ctx context.Context, id int64, name string) error
	// DeleteChannel cascades to ACL rows, subscriptions, cursors, messages.
	DeleteChannel(ctx context.Context, id int64) error
	// SetEvictedBefore sets the mark to max(current, seq).
	SetEvictedBefore(ctx context.Context, id int64, seq int64) error
	ACL(ctx context.Context, id int64) ([]ACLEntry, error) // insertion order
	// SetACL validates all entries, then replaces all, in one transaction.
	SetACL(ctx context.Context, id int64, entries []ACLEntry) error
	// EffectivePermissions is the union of stored rows for identity,
	// everyone, and admins (when identity is admin), plus the implicit
	// entries. An unregistered identity is not admin and gets the everyone
	// rows only (no error). ErrNotFound only for an unknown channel.
	EffectivePermissions(ctx context.Context, id int64, identity string) (Permissions, error)
	// ReadableChannels lists ids where EffectivePermissions.Read, ascending.
	ReadableChannels(ctx context.Context, identity string) ([]int64, error)
}
