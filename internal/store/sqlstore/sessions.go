package sqlstore

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jmoiron/sqlx"

	"github.com/ericfitz/agentbus2/internal/store"
)

// Sessions are re-keyed from v1 internal/bus/sessions.go (heartbeat, sweep,
// unregister) to a session row with a lease and a hashed resume token.

type sessionRow struct {
	ID                string `db:"id"`
	Identity          string `db:"identity"`
	DisplayName       string `db:"display_name"`
	Hint              string `db:"hint"`
	ResumeTokenHash   string `db:"resume_token_hash"`
	OpenedAt          int64  `db:"opened_at"`
	LeaseExpiresAt    int64  `db:"lease_expires_at"`
	ClosedAt          int64  `db:"closed_at"`
	PendingToken      string `db:"pending_token"`
	TagsCursorSeq     int64  `db:"tags_cursor_seq"`
	TagsPendingEndSeq int64  `db:"tags_pending_end_seq"`
	WatchID           string `db:"watch_id"`
}

func (r sessionRow) session() store.Session {
	return store.Session{
		ID: r.ID, Identity: r.Identity, DisplayName: r.DisplayName, Hint: r.Hint,
		ResumeTokenHash: r.ResumeTokenHash, OpenedAt: r.OpenedAt, LeaseExpiresAt: r.LeaseExpiresAt,
		ClosedAt: r.ClosedAt, PendingToken: r.PendingToken, TagsCursorSeq: r.TagsCursorSeq,
		TagsPendingEndSeq: r.TagsPendingEndSeq, WatchID: r.WatchID,
	}
}

const sessionCols = "id, identity, display_name, hint, resume_token_hash, opened_at, lease_expires_at, closed_at, pending_token, tags_cursor_seq, tags_pending_end_seq, watch_id"

// sessionUpdated maps an UPDATE on sessions to ErrNotFound when no row
// matched.
func sessionUpdated(res sql.Result, err error, id string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: session %q", store.ErrNotFound, id)
	}
	return nil
}

// guardSessionID runs the read-only guard, then maps an id failing TextRule
// to ErrNotFound for the writes that need the session to exist.
func (t *tx) guardSessionID(id string) error {
	if err := t.writable(); err != nil {
		return err
	}
	if badText(id) {
		return fmt.Errorf("%w: session %q", store.ErrNotFound, id)
	}
	return nil
}

// OpenSession validates the session's own text (TextRule: ErrInvalid) before
// the identity it names (failing TextRule: ErrNotFound, as for an unknown
// identity).
func (t *tx) OpenSession(ctx context.Context, s store.Session) error {
	if err := t.writable(); err != nil {
		return err
	}
	if s.ResumeTokenHash == "" {
		return fmt.Errorf("%w: session %q has an empty resume token hash", store.ErrInvalid, s.ID)
	}
	if err := checkText("session", "id", s.ID, "display name", s.DisplayName, "hint", s.Hint,
		"resume token hash", s.ResumeTokenHash, "pending token", s.PendingToken, "watch id", s.WatchID); err != nil {
		return err
	}
	if badText(s.Identity) {
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, s.Identity)
	}
	_, err := t.execW(ctx, "INSERT INTO sessions ("+sessionCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.ID, s.Identity, s.DisplayName, s.Hint, s.ResumeTokenHash, s.OpenedAt, s.LeaseExpiresAt,
		s.ClosedAt, s.PendingToken, s.TagsCursorSeq, s.TagsPendingEndSeq, s.WatchID)
	switch {
	case isConflict(err):
		return fmt.Errorf("%w: session %q (duplicate id or resume token hash)", store.ErrConflict, s.ID)
	case isForeignKey(err):
		return fmt.Errorf("%w: identity %q", store.ErrNotFound, s.Identity)
	}
	return err
}

func (t *tx) sessionBy(ctx context.Context, col, val string) (store.Session, error) {
	if badText(val) {
		return store.Session{}, fmt.Errorf("%w: session by %s", store.ErrNotFound, col)
	}
	var r sessionRow
	err := t.get(ctx, &r, "SELECT "+sessionCols+" FROM sessions WHERE "+col+" = ?", val)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Session{}, fmt.Errorf("%w: session by %s", store.ErrNotFound, col)
	}
	if err != nil {
		return store.Session{}, err
	}
	return r.session(), nil
}

func (t *tx) Session(ctx context.Context, id string) (store.Session, error) {
	return t.sessionBy(ctx, "id", id)
}

func (t *tx) SessionByResumeTokenHash(ctx context.Context, hash string) (store.Session, error) {
	return t.sessionBy(ctx, "resume_token_hash", hash)
}

// ListSessions orders by opened_at, then id in byte order (sorted here, not
// by ORDER BY: see sortByBytes); an identity failing TextRule has none.
func (t *tx) ListSessions(ctx context.Context, identity string) ([]store.Session, error) {
	if badText(identity) {
		return []store.Session{}, nil
	}
	var rows []sessionRow
	var err error
	if identity == "" {
		err = t.sel(ctx, &rows, "SELECT "+sessionCols+" FROM sessions")
	} else {
		err = t.sel(ctx, &rows, "SELECT "+sessionCols+" FROM sessions WHERE identity = ?", identity)
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b sessionRow) int {
		if c := cmp.Compare(a.OpenedAt, b.OpenedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := make([]store.Session, len(rows))
	for i, r := range rows {
		out[i] = r.session()
	}
	return out, nil
}

// RenewLease ports the heartbeat write of v1 sessions.go:534-541.
func (t *tx) RenewLease(ctx context.Context, id string, until int64) error {
	if err := t.guardSessionID(id); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE sessions SET lease_expires_at = ? WHERE id = ?", until, id)
	return sessionUpdated(res, err, id)
}

// CloseSession ports Unregister (v1 sessions.go:567).
func (t *tx) CloseSession(ctx context.Context, id string, at int64) error {
	if err := t.guardSessionID(id); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE sessions SET closed_at = ? WHERE id = ?", at, id)
	return sessionUpdated(res, err, id)
}

// DeleteSessions ports the sweep of lapsed rows (v1 sessions.go:235): select
// the keys first (no LIMIT inside DELETE), then delete by id; the cursors
// go by ON DELETE CASCADE. ErrInvalid on a limit below 1. Rows with equal
// lease_expires_at at the limit boundary are taken in id order under the
// engine's collation, so which of those goes first may differ by engine; all
// lapsed rows go eventually.
func (t *tx) DeleteSessions(ctx context.Context, lapsedBefore int64, limit int) (int, error) {
	if err := t.writable(); err != nil {
		return 0, err
	}
	if err := checkLimit(limit); err != nil {
		return 0, err
	}
	var ids []string
	if err := t.selW(ctx, &ids, "SELECT id FROM sessions WHERE lease_expires_at < ? ORDER BY lease_expires_at, id LIMIT ?", lapsedBefore, limit); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	q, args, err := sqlx.In("DELETE FROM sessions WHERE id IN (?)", ids)
	if err != nil {
		return 0, err
	}
	res, err := t.execW(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// RegisterWatch replaces the registered watch and renews the lease
// (Decisions 42, 68). ErrInvalid on a watch id failing TextRule.
func (t *tx) RegisterWatch(ctx context.Context, id, watchID string, leaseUntil int64) error {
	if err := t.writable(); err != nil {
		return err
	}
	if err := checkText("session", "watch id", watchID); err != nil {
		return err
	}
	if err := t.guardSessionID(id); err != nil {
		return err
	}
	res, err := t.execW(ctx, "UPDATE sessions SET watch_id = ?, lease_expires_at = ? WHERE id = ?", watchID, leaseUntil, id)
	if err := sessionUpdated(res, err, id); err != nil {
		return err
	}
	if n, ok := t.d.(ControlNotifier); ok {
		return n.NotifyControl(ctx, t.tx)
	}
	return nil
}
