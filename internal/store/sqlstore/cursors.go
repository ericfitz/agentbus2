package sqlstore

import (
	"context"
	"fmt"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Per-session cursors and the pending batch are ported from v1
// internal/bus/receive.go:249 (cursor rows), 336, 561 and 595 (the pending
// end per subscription and the batch token), keyed by session.

type cursorRow struct {
	SessionID     string `db:"session_id"`
	ChannelID     int64  `db:"channel_id"`
	CursorSeq     int64  `db:"cursor_seq"`
	PendingEndSeq int64  `db:"pending_end_seq"`
}

func (t *tx) SetPendingToken(ctx context.Context, id, token string) error {
	res, err := t.execW(ctx, "UPDATE sessions SET pending_token = ? WHERE id = ?", token, id)
	return sessionUpdated(res, err, id)
}

func (t *tx) SetTagsCursor(ctx context.Context, id string, cursorSeq, pendingEndSeq int64) error {
	res, err := t.execW(ctx, "UPDATE sessions SET tags_cursor_seq = ?, tags_pending_end_seq = ? WHERE id = ?", cursorSeq, pendingEndSeq, id)
	return sessionUpdated(res, err, id)
}

func (t *tx) Cursors(ctx context.Context, sessionID string) ([]store.Cursor, error) {
	var rows []cursorRow
	if err := t.sel(ctx, &rows, "SELECT session_id, channel_id, cursor_seq, pending_end_seq FROM session_cursors WHERE session_id = ? ORDER BY channel_id", sessionID); err != nil {
		return nil, err
	}
	out := make([]store.Cursor, len(rows))
	for i, r := range rows {
		out[i] = store.Cursor{SessionID: r.SessionID, ChannelID: r.ChannelID, CursorSeq: r.CursorSeq, PendingEndSeq: r.PendingEndSeq}
	}
	return out, nil
}

// PutCursor upserts; an unknown session or channel fails the foreign key and
// maps to ErrNotFound.
func (t *tx) PutCursor(ctx context.Context, c store.Cursor) error {
	_, err := t.execW(ctx, `INSERT INTO session_cursors (session_id, channel_id, cursor_seq, pending_end_seq) VALUES (?, ?, ?, ?)
ON CONFLICT (session_id, channel_id) DO UPDATE SET cursor_seq = excluded.cursor_seq, pending_end_seq = excluded.pending_end_seq`,
		c.SessionID, c.ChannelID, c.CursorSeq, c.PendingEndSeq)
	if isForeignKey(err) {
		return fmt.Errorf("%w: session %q or channel %d", store.ErrNotFound, c.SessionID, c.ChannelID)
	}
	return err
}

// DeleteCursor removes the row; an absent row is not an error.
func (t *tx) DeleteCursor(ctx context.Context, sessionID string, channelID int64) error {
	_, err := t.execW(ctx, "DELETE FROM session_cursors WHERE session_id = ? AND channel_id = ?", sessionID, channelID)
	return err
}
