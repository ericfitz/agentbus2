package bus

import "context"

// CreateChannelInput creates a channel. Kind "" means ordinary; ACL nil means
// the configured default and [] an empty list. Scope "" takes the creator's
// scope, if any; only an administrator may name one (Slack bridge spec 4.2).
type CreateChannelInput struct {
	Name           string
	Kind           Kind
	ACL            []ACLEntry
	Scope          string
	IdempotencyKey string
}

// DeleteChannelInput deletes the named channel (Decision 89).
type DeleteChannelInput struct{ Name, IdempotencyKey string }

// RenameChannelInput renames a channel (Decision 89).
type RenameChannelInput struct{ Name, NewName, IdempotencyKey string }

// SetACLInput replaces a channel's ACL (Decision 80).
type SetACLInput struct {
	Channel        string
	Entries        []ACLEntry
	IdempotencyKey string
}

// SubscribeInput subscribes the caller to a channel. From is "", "now" or "oldest".
type SubscribeInput struct {
	Channel        string
	From           string
	IdempotencyKey string
}

// UnsubscribeInput removes a subscription (Decision 95).
type UnsubscribeInput struct{ Channel, IdempotencyKey string }

// TagSubscribeInput adds or removes one AND set of tag patterns.
type TagSubscribeInput struct {
	Patterns       []string
	IdempotencyKey string
}

// SendInput sends a message. Session is optional: the caller's session id,
// for attribution and own-message exclusion.
type SendInput struct {
	Channel        string
	Subject        string
	Content        string
	Type           string
	ReplyTo        *int64
	Metadata       map[string]string
	Tags           []string
	Session        string
	IdempotencyKey string
}

// SendResult is the sequence number of the new message, and the memory id on
// a memory channel.
type SendResult struct {
	Seq      int64  `json:"seq"`
	MemoryID *int64 `json:"memory_id,omitempty"`
}

// HistoryInput reads a window of a channel. Thread, when set (at least 1),
// keeps only the message with that seq and the messages whose reply_to is it;
// on a bridged channel every reply points at the root (Decision 14).
type HistoryInput struct {
	Channel string
	Before  *int64
	After   *int64
	Count   int
	Tags    []string
	Thread  *int64
}

// SearchInput searches readable channels. Mode is "", "text", "semantic" or "both".
type SearchInput struct {
	Query   string
	Mode    string
	Channel string
	Sender  string
	Since   *int64
	Until   *int64
	Thread  *int64
	Tags    []string
	Count   int
	Cursor  string
}

// SearchHit is a message with its score.
type SearchHit struct {
	Message
	Score float64 `json:"score"`
}

// SearchResult is one page of hits.
type SearchResult struct {
	Hits                []SearchHit `json:"hits"`
	Next                string      `json:"next,omitempty"`
	SemanticUnavailable bool        `json:"semantic_unavailable,omitempty"`
}

// Messaging is the channel, subscription, send, history and search seam.
type Messaging interface {
	ListChannels(ctx context.Context) ([]Channel, error)
	CreateChannel(ctx context.Context, in CreateChannelInput) (ChannelInfo, error)
	DeleteChannel(ctx context.Context, in DeleteChannelInput) error
	RenameChannel(ctx context.Context, in RenameChannelInput) error
	ACL(ctx context.Context, channel string) ([]ACLEntry, error)
	SetACL(ctx context.Context, in SetACLInput) error
	Subscribe(ctx context.Context, in SubscribeInput) error
	Unsubscribe(ctx context.Context, in UnsubscribeInput) error
	SubscribeTags(ctx context.Context, in TagSubscribeInput) error
	UnsubscribeTags(ctx context.Context, in TagSubscribeInput) error
	TagSubscriptions(ctx context.Context) ([][]string, error)
	Send(ctx context.Context, in SendInput) (SendResult, error)
	History(ctx context.Context, in HistoryInput) ([]Message, error)
	Search(ctx context.Context, in SearchInput) (SearchResult, error)
}
