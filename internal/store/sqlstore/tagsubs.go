package sqlstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/tags.go (SubscribeTags, UnsubscribeTags,
// tagSets, tagCond; v1 ADR 0009). The tags/ pseudo-subscription row is gone
// (the session and identity columns replace it); the channel scoping is the
// caller's channelIDs list (Decision 95).
//
// tagCond's GROUP BY / HAVING count(DISTINCT) is expressed as one
// intersection of per-pattern subqueries per set instead: a message matches a
// set when every pattern is matched by some tag of the message, which is the
// same predicate. The stored lo/hi are bound as parameters, not joined as
// columns, so Postgres compares them under message_tags.tag's "C" collation
// instead of failing on a collation conflict.

type tagSetRow struct {
	TagsKey    string `db:"tags_key"`
	CreatedSeq int64  `db:"created_seq"`
}

type tagPatternRow struct {
	TagsKey string `db:"tags_key"`
	Tag     string `db:"tag"`
	Lo      string `db:"lo"`
	Hi      string `db:"hi"`
}

// SubscribeTags inserts the set and its pattern rows; no-op when present.
// ErrNotFound on an unknown identity.
func (t *tx) SubscribeTags(ctx context.Context, s store.TagSubscription) error {
	if len(s.Patterns) == 0 {
		return fmt.Errorf("%w: a tag subscription needs at least one pattern", store.ErrInvalid)
	}
	res, err := t.execW(ctx, `INSERT INTO tag_subscriptions (identity, tags_key, created_seq) VALUES (?, ?, ?)
ON CONFLICT (identity, tags_key) DO NOTHING`, s.Identity, s.TagsKey, s.CreatedSeq)
	if isForeignKey(err) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, s.Identity)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil // present: the first set and its patterns stand
	}
	for _, p := range s.Patterns {
		if _, err := t.execW(ctx, "INSERT INTO tag_subscription_tags (identity, tags_key, tag, lo, hi) VALUES (?, ?, ?, ?, ?) ON CONFLICT (identity, tags_key, tag) DO NOTHING",
			s.Identity, s.TagsKey, p.Pattern, p.Lo, p.Hi); err != nil {
			return err
		}
	}
	return nil
}

// UnsubscribeTags removes the set; its pattern rows go by cascade.
func (t *tx) UnsubscribeTags(ctx context.Context, identity, tagsKey string) error {
	_, err := t.execW(ctx, "DELETE FROM tag_subscriptions WHERE identity = ? AND tags_key = ?", identity, tagsKey)
	return err
}

// TagSubscriptions lists the identity's sets in byte order of the key,
// patterns in byte order of the pattern (sorted here, not by ORDER BY: see
// sortByBytes).
func (t *tx) TagSubscriptions(ctx context.Context, identity string) ([]store.TagSubscription, error) {
	var sets []tagSetRow
	if err := t.sel(ctx, &sets, "SELECT tags_key, created_seq FROM tag_subscriptions WHERE identity = ?", identity); err != nil {
		return nil, err
	}
	sortByBytes(sets, func(s tagSetRow) string { return s.TagsKey })
	var pats []tagPatternRow
	if err := t.sel(ctx, &pats, "SELECT tags_key, tag, lo, hi FROM tag_subscription_tags WHERE identity = ?", identity); err != nil {
		return nil, err
	}
	// Grouped by set below, so the order within one set is what counts; a
	// pattern is unique within its set.
	sortByBytes(pats, func(p tagPatternRow) string { return p.Tag })
	out := make([]store.TagSubscription, len(sets))
	idx := make(map[string]int, len(sets))
	for i, s := range sets {
		out[i] = store.TagSubscription{Identity: identity, TagsKey: s.TagsKey, CreatedSeq: s.CreatedSeq}
		idx[s.TagsKey] = i
	}
	for _, p := range pats {
		if i, ok := idx[p.TagsKey]; ok {
			out[i].Patterns = append(out[i].Patterns, store.TagPattern{Pattern: p.Tag, Lo: p.Lo, Hi: p.Hi})
		}
	}
	return out, nil
}

// TagMatchesAfter returns the live messages on channelIDs with seq > afterSeq
// that satisfy at least one of the identity's sets (every pattern of the set
// matched by some tag, seq above max(afterSeq, set created_seq)), ascending,
// at most limit.
func (t *tx) TagMatchesAfter(ctx context.Context, identity string, afterSeq int64, channelIDs []int64, limit int) ([]store.Message, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	if len(channelIDs) == 0 {
		return []store.Message{}, nil
	}
	sets, err := t.TagSubscriptions(ctx, identity)
	if err != nil {
		return nil, err
	}
	var branches []string
	var branchArgs []any
	for _, s := range sets {
		if len(s.Patterns) == 0 {
			continue
		}
		floor := max(afterSeq, s.CreatedSeq)
		conds := make([]string, len(s.Patterns))
		for i, p := range s.Patterns {
			conds[i] = "m.seq IN (SELECT mt.seq FROM message_tags mt WHERE mt.tag BETWEEN ? AND ? AND mt.seq > ?)"
			branchArgs = append(branchArgs, p.Lo, p.Hi, floor)
		}
		branches = append(branches, "("+strings.Join(conds, " AND ")+")")
	}
	if len(branches) == 0 {
		return []store.Message{}, nil
	}
	args := []any{channelIDs, afterSeq}
	args = append(args, branchArgs...)
	args = append(args, limit)
	q, args, err := sqlx.In("SELECT "+messageCols+" FROM messages m WHERE m.tombstone = FALSE AND m.channel_id IN (?) AND m.seq > ? AND ("+strings.Join(branches, " OR ")+") ORDER BY m.seq LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	return scanMessages(ctx, t, q, args...)
}
