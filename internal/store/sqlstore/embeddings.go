package sqlstore

import (
	"context"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/embed.go: unembeddedFrom (task rows are now
// excluded by channels.kind, Decision 90), the stale-model delete, and the
// conditional put (only for a live memory). encodeVec and decodeVec stay in
// the core; the Store holds bytes.

// The model names the embedder's configuration, not a stored row: a model
// failing TextRule is ErrInvalid everywhere but Embeddings, a lookup that
// finds none.

func (t *tx) UnembeddedMemories(ctx context.Context, model string, limit int) ([]store.Message, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	if err := checkText("embedding", "model", model); err != nil {
		return nil, err
	}
	return scanMessages(ctx, t, `SELECT `+messageCols+` FROM messages m
		JOIN channels c ON c.id = m.channel_id
		LEFT JOIN embeddings e ON e.seq = m.seq AND e.model = ?
		WHERE m.memory_id IS NOT NULL AND m.tombstone = FALSE AND e.seq IS NULL AND c.kind <> 'tasks'
		AND NOT EXISTS (SELECT 1 FROM embed_failures f WHERE f.seq = m.seq AND f.model = ?)
		ORDER BY m.seq LIMIT ?`, model, model, limit)
}

// liveMemory reports whether seq is a live memory row.
func (t *tx) liveMemory(ctx context.Context, seq int64) (bool, error) {
	var n int
	if err := t.get(ctx, &n, "SELECT count(*) FROM messages WHERE seq = ? AND memory_id IS NOT NULL AND tombstone = FALSE", seq); err != nil {
		return false, err
	}
	return n > 0, nil
}

func (t *tx) PutEmbedding(ctx context.Context, e store.Embedding) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("embedding", "model", e.Model); err != nil {
		return err
	}
	ok, err := t.liveMemory(ctx, e.Seq)
	if err != nil || !ok {
		return err
	}
	_, err = t.execW(ctx, `INSERT INTO embeddings (seq, model, vector) VALUES (?, ?, ?)
		ON CONFLICT (seq) DO UPDATE SET model = excluded.model, vector = excluded.vector`, e.Seq, e.Model, e.Vector)
	return err
}

func (t *tx) PutEmbedFailure(ctx context.Context, f store.EmbedFailure) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("embed failure", "model", f.Model, "error", f.Error); err != nil {
		return err
	}
	ok, err := t.liveMemory(ctx, f.Seq)
	if err != nil || !ok {
		return err
	}
	_, err = t.execW(ctx, `INSERT INTO embed_failures (seq, model, error, failed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (seq, model) DO UPDATE SET error = excluded.error, failed_at = excluded.failed_at`,
		f.Seq, f.Model, f.Error, f.FailedAt)
	return err
}

func (t *tx) DeleteEmbedding(ctx context.Context, seq int64) error {
	_, err := t.execW(ctx, "DELETE FROM embeddings WHERE seq = ?", seq)
	return err
}

func (t *tx) DeleteStaleModel(ctx context.Context, model string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("embedding", "model", model); err != nil {
		return err
	}
	if _, err := t.execW(ctx, "DELETE FROM embeddings WHERE model <> ?", model); err != nil {
		return err
	}
	_, err := t.execW(ctx, "DELETE FROM embed_failures WHERE model <> ?", model)
	return err
}

func (t *tx) Embeddings(ctx context.Context, model string, channelIDs []int64) ([]store.Embedding, error) {
	out := []store.Embedding{}
	if len(channelIDs) == 0 || badText(model) {
		return out, nil
	}
	q, args, err := sqlx.In(`SELECT e.seq, e.model, e.vector FROM embeddings e JOIN messages m ON m.seq = e.seq
		WHERE e.model = ? AND m.memory_id IS NOT NULL AND m.tombstone = FALSE AND m.channel_id IN (?) ORDER BY e.seq`, model, channelIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Seq    int64  `db:"seq"`
		Model  string `db:"model"`
		Vector []byte `db:"vector"`
	}
	if err := t.sel(ctx, &rows, q, args...); err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, store.Embedding{Seq: r.Seq, Model: r.Model, Vector: r.Vector})
	}
	return out, nil
}
