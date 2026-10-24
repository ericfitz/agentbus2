package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/search"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Ported from v1 internal/bus/messages.go (Message, messageCols; the
// group_concat tag subquery is a second query), internal/bus/tags.go
// (insertTags, tagsOf, tagsFilter) and internal/bus/maintenance.go (expired
// tombstones, deleteOrdinaryChunk: select the keys, then delete them;
// evicted_before_seq is the caller's through SetEvictedBefore).

// messageCols is the fixed, alias-qualified column list; every query that
// selects messages names the table "messages m" and selects exactly
// messageCols (Tasks 8 and 9 reuse both).
const messageCols = "m.seq, m.channel_id, m.sender, m.session_id, m.sender_kind, m.created_at, m.type, m.subject, m.content, m.reply_to, m.metadata, m.memory_id, m.revision, m.tombstone, m.tombstone_at"

// idChunk bounds the bind variables of one IN list (SQLite allows 32766).
const idChunk = 500

type messageRow struct {
	Seq         int64          `db:"seq"`
	ChannelID   int64          `db:"channel_id"`
	Sender      string         `db:"sender"`
	SessionID   string         `db:"session_id"`
	SenderKind  string         `db:"sender_kind"`
	CreatedAt   int64          `db:"created_at"`
	Type        string         `db:"type"`
	Subject     string         `db:"subject"`
	Content     string         `db:"content"`
	ReplyTo     *int64         `db:"reply_to"`
	Metadata    sql.NullString `db:"metadata"`
	MemoryID    *int64         `db:"memory_id"`
	Revision    *int64         `db:"revision"`
	Tombstone   bool           `db:"tombstone"`
	TombstoneAt *int64         `db:"tombstone_at"`
}

func (r messageRow) message() (store.Message, error) {
	m := store.Message{
		Seq: r.Seq, ChannelID: r.ChannelID, Sender: r.Sender, SessionID: r.SessionID, SenderKind: store.IdentityKind(r.SenderKind),
		CreatedAt: r.CreatedAt, Type: r.Type, Subject: r.Subject, Content: r.Content,
		ReplyTo: r.ReplyTo, MemoryID: r.MemoryID, Revision: r.Revision,
		Tombstone: r.Tombstone, TombstoneAt: r.TombstoneAt,
	}
	if r.Metadata.Valid {
		if err := json.Unmarshal([]byte(r.Metadata.String), &m.Metadata); err != nil {
			return store.Message{}, fmt.Errorf("message %d metadata: %w", r.Seq, err)
		}
	}
	return m, nil
}

// scanMessages runs query (which selects messageCols from "messages m"),
// loads Tags with the second query over message_tags, and returns the rows in
// the query's order. query uses ? placeholders.
func scanMessages(ctx context.Context, t *tx, query string, args ...any) ([]store.Message, error) {
	var rows []messageRow
	if err := t.sel(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	out := make([]store.Message, len(rows))
	idx := make(map[int64]int, len(rows))
	seqs := make([]int64, len(rows))
	for i, r := range rows {
		m, err := r.message()
		if err != nil {
			return nil, err
		}
		out[i] = m
		idx[m.Seq] = i
		seqs[i] = m.Seq
	}
	for chunk := range slices.Chunk(seqs, idChunk) {
		q, qargs, err := sqlx.In("SELECT seq, tag FROM message_tags WHERE seq IN (?) ORDER BY seq, tag", chunk)
		if err != nil {
			return nil, err
		}
		var tags []struct {
			Seq int64  `db:"seq"`
			Tag string `db:"tag"`
		}
		if err := t.sel(ctx, &tags, q, qargs...); err != nil {
			return nil, err
		}
		for _, tg := range tags {
			i := idx[tg.Seq]
			out[i].Tags = append(out[i].Tags, tg.Tag)
		}
	}
	return out, nil
}

func checkLimit(limit int) error {
	if limit <= 0 {
		return fmt.Errorf("%w: limit %d must be positive", store.ErrInvalid, limit)
	}
	return nil
}

// checkMessageText runs TextRule over every TEXT the message stores. The
// metadata is marshaled to one JSON column, which would escape a NUL and
// replace invalid UTF-8 silently; it is refused instead of stored altered.
func checkMessageText(m store.NewMessage) error {
	if err := checkText("message", "sender", m.Sender, "session id", m.SessionID, "type", m.Type, "subject", m.Subject, "content", m.Content); err != nil {
		return err
	}
	for _, tag := range m.Tags {
		if err := checkText("message", "tag", tag); err != nil {
			return err
		}
	}
	for _, k := range slices.Sorted(maps.Keys(m.Metadata)) {
		if err := checkText("message", "metadata key", k, "metadata value", m.Metadata[k]); err != nil {
			return err
		}
	}
	return nil
}

// AppendMessage runs the read-only and append guards, then checkMessageText,
// before NextSeq allocates.
func (t *tx) AppendMessage(ctx context.Context, m store.NewMessage) (int64, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if !t.append {
		return 0, store.ErrNotAppend
	}
	if err := checkMessageText(m); err != nil {
		return 0, err
	}
	if m.SenderKind == "" {
		m.SenderKind = store.IdentityAgent
	}
	if !store.ValidIdentityKind(m.SenderKind) {
		return 0, fmt.Errorf("%w: message sender kind %q (want agent or human)", store.ErrInvalid, m.SenderKind)
	}
	seq, err := t.NextSeq(ctx)
	if err != nil {
		return 0, err
	}
	var meta any
	if len(m.Metadata) > 0 {
		b, err := json.Marshal(m.Metadata)
		if err != nil {
			return 0, err
		}
		meta = string(b)
	}
	_, err = t.execW(ctx, `INSERT INTO messages (seq, channel_id, sender, session_id, sender_kind, created_at, type, subject, content, reply_to, metadata, memory_id, revision, tombstone)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, FALSE)`,
		seq, m.ChannelID, m.Sender, m.SessionID, string(m.SenderKind), m.CreatedAt, m.Type, m.Subject, m.Content,
		m.ReplyTo, meta, m.MemoryID, m.Revision)
	if isForeignKey(err) {
		return 0, fmt.Errorf("%w: channel %d", store.ErrNotFound, m.ChannelID)
	}
	if err != nil {
		return 0, err
	}
	// A message is activity in its channel's scope (an unscoped channel has no
	// scope row to touch).
	if _, err := t.execW(ctx, "UPDATE scopes SET last_activity = ? WHERE name = (SELECT scope FROM channels WHERE id = ?) AND last_activity < ?",
		m.CreatedAt, m.ChannelID, m.CreatedAt); err != nil {
		return 0, err
	}
	tags := slices.Clone(m.Tags)
	slices.Sort(tags)
	for _, tag := range slices.Compact(tags) {
		if _, err := t.execW(ctx, "INSERT INTO message_tags (seq, tag) VALUES (?, ?)", seq, tag); err != nil {
			return 0, err
		}
	}
	if err := t.d.NotifyAppend(ctx, t.tx, m.ChannelID, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

func (t *tx) Message(ctx context.Context, seq int64) (store.Message, error) {
	ms, err := scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.seq = ?", seq)
	if err != nil {
		return store.Message{}, err
	}
	if len(ms) == 0 {
		return store.Message{}, fmt.Errorf("%w: message %d", store.ErrNotFound, seq)
	}
	return ms[0], nil
}

// excludeSQL is v1 receive.go ownClause re-keyed to sender and session_id: an
// AND clause per set field. A value failing TextRule matches no stored row,
// so excluding it excludes nothing and adds no clause (Postgres would refuse
// a NUL parameter).
func excludeSQL(f store.MessageFilter) (string, []any) {
	var q string
	var args []any
	if f.ExcludeSender != "" && !badText(f.ExcludeSender) {
		q += " AND m.sender <> ?"
		args = append(args, f.ExcludeSender)
	}
	if f.ExcludeSessionID != "" && !badText(f.ExcludeSessionID) {
		q += " AND m.session_id <> ?"
		args = append(args, f.ExcludeSessionID)
	}
	return q, args
}

// tagsFilterSQL is v1 tagsFilter: one EXISTS per pattern, ANDed. ok is false
// when a pattern's range fails TextRule (it matches no stored tag).
func tagsFilterSQL(patterns []store.TagPattern) (q string, args []any, ok bool) {
	for _, p := range patterns {
		if badText(p.Lo, p.Hi) {
			return "", nil, false
		}
		q += " AND EXISTS (SELECT 1 FROM message_tags g WHERE g.seq = m.seq AND g.tag BETWEEN ? AND ?)"
		args = append(args, p.Lo, p.Hi)
	}
	return q, args, true
}

// searchFilterSQL is v1 searchFilters over alias m without the DM branch.
// ok is false when the filter can match nothing (a sender or pattern failing
// TextRule).
func searchFilterSQL(f store.SearchFilter) (q string, args []any, ok bool) {
	if f.Sender != "" {
		if badText(f.Sender) {
			return "", nil, false
		}
		q += " AND m.sender = ?"
		args = append(args, f.Sender)
	}
	if f.Since != nil {
		q += " AND m.created_at >= ?"
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		q += " AND m.created_at <= ?"
		args = append(args, *f.Until)
	}
	if f.Thread != nil {
		q += " AND (m.seq = ? OR m.reply_to = ?)"
		args = append(args, *f.Thread, *f.Thread)
	}
	tq, targs, ok := tagsFilterSQL(f.Patterns)
	if !ok {
		return "", nil, false
	}
	return q + tq, append(args, targs...), true
}

func (t *tx) MessagesAfter(ctx context.Context, channelID, afterSeq int64, f store.MessageFilter, limit int) ([]store.Message, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	eq, eargs := excludeSQL(f)
	args := append([]any{channelID, afterSeq}, eargs...)
	return scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.channel_id = ? AND m.seq > ? AND m.tombstone = FALSE"+eq+" ORDER BY m.seq LIMIT ?",
		append(args, limit)...)
}

// MessagesBySeqs loads live rows by seq in chunks, ascending.
func (t *tx) MessagesBySeqs(ctx context.Context, seqs []int64) ([]store.Message, error) {
	out := []store.Message{}
	for chunk := range slices.Chunk(slices.Sorted(slices.Values(seqs)), idChunk) {
		q, args, err := sqlx.In("SELECT "+messageCols+" FROM messages m WHERE m.seq IN (?) AND m.tombstone = FALSE ORDER BY m.seq", chunk)
		if err != nil {
			return nil, err
		}
		ms, err := scanMessages(ctx, t, q, args...)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
	}
	return out, nil
}

// MessagesBefore is v1 History with before only: the newest limit live rows
// below beforeSeq, newest first.
func (t *tx) MessagesBefore(ctx context.Context, channelID, beforeSeq int64, patterns []store.TagPattern, f store.MessageFilter, limit int) ([]store.Message, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	tq, targs, ok := tagsFilterSQL(patterns)
	if !ok {
		return []store.Message{}, nil
	}
	eq, eargs := excludeSQL(f)
	args := append([]any{channelID, beforeSeq}, targs...)
	args = append(args, eargs...)
	return scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.channel_id = ? AND m.seq < ? AND m.tombstone = FALSE"+tq+eq+" ORDER BY m.seq DESC LIMIT ?",
		append(args, limit)...)
}

// MessagesInRange applies v1 tagsFilter: one EXISTS per pattern, ANDed. A
// pattern whose range fails TextRule matches no stored tag, so the result is
// empty without a query.
func (t *tx) MessagesInRange(ctx context.Context, channelID, fromSeq, toSeq int64, patterns []store.TagPattern, f store.MessageFilter, limit int) ([]store.Message, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	tq, targs, ok := tagsFilterSQL(patterns)
	if !ok {
		return []store.Message{}, nil
	}
	eq, eargs := excludeSQL(f)
	args := append([]any{channelID, fromSeq, toSeq}, targs...)
	args = append(args, eargs...)
	return scanMessages(ctx, t, "SELECT "+messageCols+" FROM messages m WHERE m.channel_id = ? AND m.seq >= ? AND m.seq <= ? AND m.tombstone = FALSE"+tq+eq+" ORDER BY m.seq LIMIT ?",
		append(args, limit)...)
}

// SearchText: the one query shape, engine-independent; the dialect supplies
// the condition on alias m. A term failing TextRule is ErrInvalid (search.Parse
// keeps a NUL inside a word). Newest first, so limit keeps the newest matches.
func (t *tx) SearchText(ctx context.Context, q search.Query, channelIDs []int64, f store.SearchFilter, limit int) ([]store.Message, error) {
	if q.Empty() {
		return nil, fmt.Errorf("%w: search query has no searchable terms", store.ErrInvalid)
	}
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	for _, term := range q.Terms {
		if err := checkText("search", "term", term.Text); err != nil {
			return nil, err
		}
	}
	if len(channelIDs) == 0 {
		return []store.Message{}, nil
	}
	fq, fargs, ok := searchFilterSQL(f)
	if !ok {
		return []store.Message{}, nil
	}
	cond, condArgs, err := t.d.TextSearch(q)
	if err != nil {
		return nil, err
	}
	// sqlx.In expands the channel list; the condition's own placeholders come
	// first in the argument order.
	// The filter's placeholders follow the channel list, in SQL order.
	qargs := append(slices.Clone(condArgs), channelIDs)
	qargs = append(append(qargs, fargs...), limit)
	query, args, err := sqlx.In("SELECT "+messageCols+" FROM messages m WHERE ("+cond+") AND m.tombstone = FALSE AND m.channel_id IN (?)"+fq+" ORDER BY m.seq DESC LIMIT ?",
		qargs...)
	if err != nil {
		return nil, err
	}
	return scanMessages(ctx, t, query, args...)
}

func (t *tx) TombstoneMessage(ctx context.Context, seq, at int64) (bool, error) {
	res, err := t.execW(ctx, "UPDATE messages SET tombstone = TRUE, tombstone_at = ? WHERE seq = ? AND tombstone = FALSE", at, seq)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (t *tx) DeleteMessages(ctx context.Context, seqs []int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	for chunk := range slices.Chunk(seqs, idChunk) {
		q, args, err := sqlx.In("DELETE FROM messages WHERE seq IN (?)", chunk)
		if err != nil {
			return err
		}
		if _, err := t.execW(ctx, q, args...); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) ExpiredTombstones(ctx context.Context, before int64, limit int) ([]int64, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	out := []int64{}
	err := t.sel(ctx, &out, "SELECT seq FROM messages WHERE tombstone = TRUE AND tombstone_at < ? ORDER BY seq LIMIT ?", before, limit)
	return out, err
}

func (t *tx) OrdinaryMessagesBefore(ctx context.Context, createdBefore int64, limit int) ([]store.SeqChannel, error) {
	if err := checkLimit(limit); err != nil {
		return nil, err
	}
	var rows []struct {
		Seq       int64 `db:"seq"`
		ChannelID int64 `db:"channel_id"`
	}
	if err := t.sel(ctx, &rows, "SELECT seq, channel_id FROM messages WHERE memory_id IS NULL AND created_at < ? ORDER BY seq LIMIT ?", createdBefore, limit); err != nil {
		return nil, err
	}
	out := make([]store.SeqChannel, len(rows))
	for i, r := range rows {
		out[i] = store.SeqChannel{Seq: r.Seq, ChannelID: r.ChannelID}
	}
	return out, nil
}
