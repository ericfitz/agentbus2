package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/receive.go (Subscribe: insert-or-ignore;
// Unsubscribe), keyed by the identity (Decision 37) with the acked mark of
// Decision 75.

type subscriptionRow struct {
	Identity  string `db:"identity"`
	ChannelID int64  `db:"channel_id"`
	AckedSeq  int64  `db:"acked_seq"`
	CreatedAt int64  `db:"created_at"`
}

// Subscribe is a no-op when the subscription exists (the first AckedSeq and
// CreatedAt stay); ErrNotFound on an unknown identity or channel, or an
// identity failing TextRule (every method here treats such an identity as
// unknown).
func (t *tx) Subscribe(ctx context.Context, identity string, channelID, ackedSeq, now int64) error {
	if err := t.guardIdentityName(identity); err != nil {
		return err
	}
	_, err := t.execW(ctx, `INSERT INTO subscriptions (identity, channel_id, acked_seq, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT (identity, channel_id) DO NOTHING`, identity, channelID, ackedSeq, now)
	if isForeignKey(err) {
		return fmt.Errorf("%w: identity %q or channel %d", store.ErrNotFound, identity, channelID)
	}
	return err
}

func (t *tx) Unsubscribe(ctx context.Context, identity string, channelID int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if badText(identity) {
		return nil
	}
	_, err := t.execW(ctx, "DELETE FROM subscriptions WHERE identity = ? AND channel_id = ?", identity, channelID)
	return err
}

func (t *tx) Subscriptions(ctx context.Context, identity string) ([]store.Subscription, error) {
	if badText(identity) {
		return []store.Subscription{}, nil
	}
	var rows []subscriptionRow
	if err := t.sel(ctx, &rows, "SELECT identity, channel_id, acked_seq, created_at FROM subscriptions WHERE identity = ? ORDER BY channel_id", identity); err != nil {
		return nil, err
	}
	out := make([]store.Subscription, len(rows))
	for i, r := range rows {
		out[i] = store.Subscription{Identity: r.Identity, ChannelID: r.ChannelID, AckedSeq: r.AckedSeq, CreatedAt: r.CreatedAt}
	}
	return out, nil
}

// AdvanceAckedMark sets the mark to max(current, seq); ErrNotFound when the
// identity is not subscribed to the channel.
func (t *tx) AdvanceAckedMark(ctx context.Context, identity string, channelID, seq int64) error {
	if err := t.guardIdentityName(identity); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE subscriptions SET acked_seq = CASE WHEN acked_seq < ? THEN ? ELSE acked_seq END WHERE identity = ? AND channel_id = ?", seq, seq, identity, channelID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: subscription of %q to channel %d", store.ErrNotFound, identity, channelID)
	}
	return nil
}
