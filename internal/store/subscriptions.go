package store

import "context"

// Subscription is identity-level and carries the acked mark (plan choice 7).
type Subscription struct {
	Identity            string
	ChannelID           int64
	AckedSeq, CreatedAt int64
}

// TagSubscription is a set of tag patterns under one key (Decision 95).
type TagSubscription struct {
	Identity, TagsKey string
	Patterns          []TagPattern
	CreatedSeq        int64
}

type Subscriptions interface {
	// Subscribe is a no-op when present; ErrNotFound on an unknown identity
	// or channel.
	Subscribe(ctx context.Context, identity string, channelID int64, ackedSeq, now int64) error
	Unsubscribe(ctx context.Context, identity string, channelID int64) error
	Subscriptions(ctx context.Context, identity string) ([]Subscription, error) // by channel id
	// AdvanceAckedMark sets the mark to max(current, seq).
	AdvanceAckedMark(ctx context.Context, identity string, channelID int64, seq int64) error
	// SubscribeTags inserts the set and its pattern rows; no-op when present.
	SubscribeTags(ctx context.Context, s TagSubscription) error
	UnsubscribeTags(ctx context.Context, identity, tagsKey string) error
	TagSubscriptions(ctx context.Context, identity string) ([]TagSubscription, error)
	// TagMatchesAfter is v1 tagCond scoped to channelIDs (Decision 95): live
	// messages only, ascending; upToSeq > 0 also requires seq <= upToSeq; f
	// excludes the sender's or session's own messages.
	TagMatchesAfter(ctx context.Context, identity string, afterSeq, upToSeq int64, channelIDs []int64, f MessageFilter, limit int) ([]Message, error)
}
