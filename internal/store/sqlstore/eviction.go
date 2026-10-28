package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// MeasuredTables lists the tables that carry a generated size_bytes column
// (store.RowOverheadBytes plus the byte length of the counted columns): every
// table an eviction delete removes rows from, directly or by cascade. The
// conformance test TestMeasuredTablesCoverCascades derives the set from the
// catalog, so a new cascade or direct delete fails it until the table is
// listed and measured.
var MeasuredTables = []string{
	"channels", "channel_acl", "subscriptions", "session_cursors",
	"messages", "message_tags", "embeddings", "embed_failures",
	"memory_access", "bridge_messages", "enrollment_tokens",
}

// sumBytes is the total size_bytes of the rows of table where cond holds, as
// a BIGINT (SUM is numeric on Postgres), 0 for none.
func sumBytes(table, cond string) string {
	return "CAST(COALESCE((SELECT SUM(x.size_bytes) FROM " + table + " x WHERE " + cond + "), 0) AS BIGINT)"
}

// messageBytesExpr is the bytes deleting message m frees: its row and the
// message_tags, embeddings and embed_failures rows that cascade from it.
var messageBytesExpr = "(m.size_bytes + " + sumBytes("message_tags", "x.seq = m.seq") +
	" + " + sumBytes("embeddings", "x.seq = m.seq") +
	" + " + sumBytes("embed_failures", "x.seq = m.seq") + ")"

func (t *tx) OrphanedBridgeMessages(ctx context.Context, limit int) ([]store.BridgeMessageRef, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var rows []struct {
		BridgeID int64 `db:"bridge_id"`
		Seq      int64 `db:"seq"`
		Bytes    int64 `db:"bytes"`
	}
	if err := t.sel(ctx, &rows, `SELECT b.bridge_id, b.seq, b.size_bytes AS bytes FROM bridge_messages b
		WHERE NOT EXISTS (SELECT 1 FROM messages m WHERE m.seq = b.seq)
		ORDER BY b.bridge_id, b.seq LIMIT ?`, limit); err != nil {
		return nil, err
	}
	out := make([]store.BridgeMessageRef, len(rows))
	for i, r := range rows {
		out[i] = store.BridgeMessageRef{BridgeID: r.BridgeID, Seq: r.Seq, Bytes: r.Bytes}
	}
	return out, nil
}

func (t *tx) DeleteBridgeMessages(ctx context.Context, refs []store.BridgeMessageRef) error {
	if err := t.writable(); err != nil {
		return err
	}
	for _, r := range refs {
		if _, err := t.execW(ctx, "DELETE FROM bridge_messages WHERE bridge_id = ? AND seq = ? AND NOT EXISTS (SELECT 1 FROM messages m WHERE m.seq = bridge_messages.seq)", r.BridgeID, r.Seq); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) ExpiredEnrollmentTokens(ctx context.Context, now int64, limit int) ([]store.TokenRef, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var rows []struct {
		ID    string `db:"id"`
		Bytes int64  `db:"bytes"`
	}
	if err := t.sel(ctx, &rows, "SELECT id, size_bytes AS bytes FROM enrollment_tokens WHERE expires_at < ? ORDER BY expires_at, id LIMIT ?", now, limit); err != nil {
		return nil, err
	}
	out := make([]store.TokenRef, len(rows))
	for i, r := range rows {
		out[i] = store.TokenRef{ID: r.ID, Bytes: r.Bytes}
	}
	return out, nil
}

func (t *tx) DeleteEnrollmentTokens(ctx context.Context, ids []string) error {
	if err := t.writable(); err != nil {
		return err
	}
	for chunk := range slices.Chunk(ids, idChunk) {
		q, args, err := sqlx.In("DELETE FROM enrollment_tokens WHERE id IN (?)", chunk)
		if err != nil {
			return err
		}
		if _, err := t.execW(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}

// channelActivityExpr is the latest activity of channel c: the greatest of its
// created_at, last_read_at and its messages' created_at and tombstone_at
// (MAX ignores NULL; a scalar max is not portable, hence the UNION).
const channelActivityExpr = `(SELECT MAX(v) FROM (
		SELECT c.created_at AS v
		UNION ALL SELECT c.last_read_at
		UNION ALL SELECT MAX(a.created_at) FROM messages a WHERE a.channel_id = c.id
		UNION ALL SELECT MAX(a.tombstone_at) FROM messages a WHERE a.channel_id = c.id) act)`

// idleTaskCond: a task list with no bridge that is not archived and no
// activity at or after the cutoff (one ?).
const idleTaskCond = `c.kind = 'tasks'
	AND NOT EXISTS (SELECT 1 FROM bridges b WHERE b.channel_id = c.id AND b.state <> 'archived')
	AND ` + channelActivityExpr + ` < ?`

func (t *tx) IdleTaskChannels(ctx context.Context, idleBefore int64, limit int) ([]store.Channel, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var rows []channelRow
	if err := t.sel(ctx, &rows, "SELECT "+channelCols+" FROM channels c WHERE "+idleTaskCond+" ORDER BY "+channelActivityExpr+", c.id LIMIT ?", idleBefore, limit); err != nil {
		return nil, err
	}
	return channelsOf(rows), nil
}

func (t *tx) ChannelBytes(ctx context.Context, channelID int64) (int64, error) {
	var n int64
	err := t.get(ctx, &n, `SELECT c.size_bytes
		+ `+sumBytes("channel_acl", "x.channel_id = c.id")+`
		+ `+sumBytes("subscriptions", "x.channel_id = c.id")+`
		+ `+sumBytes("session_cursors", "x.channel_id = c.id")+`
		+ CAST(COALESCE((SELECT SUM(`+messageBytesExpr+`) FROM messages m WHERE m.channel_id = c.id), 0) AS BIGINT)
		FROM channels c WHERE c.id = ?`, channelID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: channel %d", store.ErrNotFound, channelID)
	}
	return n, err
}

func (t *tx) DeleteIdleTaskChannel(ctx context.Context, channelID, idleBefore int64) (bool, error) {
	res, err := t.execW(ctx, "DELETE FROM channels WHERE id = ? AND id IN (SELECT c.id FROM channels c WHERE "+idleTaskCond+")", channelID, idleBefore)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (t *tx) IdleMemories(ctx context.Context, idleBefore int64, limit int) ([]store.SizedMemory, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	const act = "(CASE WHEN a.accessed_at IS NOT NULL AND a.accessed_at > l.created_at THEN a.accessed_at ELSE l.created_at END)"
	var rows []struct {
		MemoryID int64 `db:"memory_id"`
		Bytes    int64 `db:"bytes"`
	}
	q := `SELECT l.memory_id AS memory_id,
			CAST(COALESCE((SELECT SUM(` + messageBytesExpr + `) FROM messages m WHERE m.memory_id = l.memory_id), 0) AS BIGINT)
			+ COALESCE(a.size_bytes, 0) AS bytes
		FROM messages l
		JOIN channels c ON c.id = l.channel_id
		LEFT JOIN memory_access a ON a.memory_id = l.memory_id
		WHERE l.tombstone = FALSE AND l.memory_id IS NOT NULL AND c.kind <> 'tasks' AND ` + act + ` < ?
		ORDER BY ` + act + `, l.memory_id LIMIT ?`
	if err := t.sel(ctx, &rows, q, idleBefore, limit); err != nil {
		return nil, err
	}
	out := make([]store.SizedMemory, len(rows))
	for i, r := range rows {
		out[i] = store.SizedMemory{MemoryID: r.MemoryID, Bytes: r.Bytes}
	}
	return out, nil
}

func (t *tx) PurgeMemory(ctx context.Context, memoryID int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if _, err := t.execW(ctx, "DELETE FROM messages WHERE memory_id = ?", memoryID); err != nil {
		return err
	}
	_, err := t.execW(ctx, "DELETE FROM memory_access WHERE memory_id = ?", memoryID)
	return err
}

func (t *tx) EvictionLeft(ctx context.Context, c store.EvictionCutoffs) (store.EvictionLeft, error) {
	var l store.EvictionLeft
	orphans, err := t.OrphanedBridgeMessages(ctx, 1)
	if err != nil {
		return l, err
	}
	l.Useless = len(orphans) > 0
	tombs, err := t.ExpiredTombstones(ctx, c.TombstoneBefore, 1)
	if err != nil {
		return l, err
	}
	tokens, err := t.ExpiredEnrollmentTokens(ctx, c.TokenExpiredBefore, 1)
	if err != nil {
		return l, err
	}
	l.Expired = len(tombs) > 0 || len(tokens) > 0
	chans, err := t.IdleTaskChannels(ctx, c.IdleBefore, 1)
	if err != nil {
		return l, err
	}
	mems, err := t.IdleMemories(ctx, c.IdleBefore, 1)
	if err != nil {
		return l, err
	}
	l.Idle = len(chans) > 0 || len(mems) > 0
	ords, err := t.OrdinaryMessagesBefore(ctx, c.MessageBefore, 1)
	if err != nil {
		return l, err
	}
	l.Ordinary = len(ords) > 0
	return l, nil
}
