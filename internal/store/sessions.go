package store

import "context"

// Session is an agent session (Decisions 40, 42, 68, 79).
type Session struct {
	ID, Identity, DisplayName, Hint string
	// ResumeTokenHash is the core's hash of the resume token, never the
	// token (plan choice 12).
	ResumeTokenHash                    string
	OpenedAt, LeaseExpiresAt, ClosedAt int64
	PendingToken                       string // the one pending batch per session (plan choice 7)
	TagsCursorSeq, TagsPendingEndSeq   int64
	WatchID                            string
}

// Cursor is per-session, per-channel delivery state (Decisions 16, 37).
type Cursor struct {
	SessionID                           string
	ChannelID, CursorSeq, PendingEndSeq int64
}

type Sessions interface {
	// OpenSession: ErrConflict on id or resume token hash; ErrNotFound on
	// identity; ErrInvalid on an empty ResumeTokenHash.
	OpenSession(ctx context.Context, s Session) error
	Session(ctx context.Context, id string) (Session, error)
	SessionByResumeTokenHash(ctx context.Context, hash string) (Session, error)
	// ListSessions: identity "" means every identity; by opened_at, then id
	// in byte order.
	ListSessions(ctx context.Context, identity string) ([]Session, error)
	RenewLease(ctx context.Context, id string, until int64) error
	CloseSession(ctx context.Context, id string, at int64) error
	// DeleteSessions removes sessions with lease_expires_at < lapsedBefore
	// (Decision 79 retention); cascades cursors. ErrInvalid on a limit below
	// 1.
	DeleteSessions(ctx context.Context, lapsedBefore int64, limit int) (int, error)
	// RegisterWatch sets watch_id and renews the lease (Decisions 42, 68).
	RegisterWatch(ctx context.Context, id, watchID string, leaseUntil int64) error
	SetPendingToken(ctx context.Context, id, token string) error
	SetTagsCursor(ctx context.Context, id string, cursorSeq, pendingEndSeq int64) error
	Cursors(ctx context.Context, sessionID string) ([]Cursor, error) // by channel id
	PutCursor(ctx context.Context, c Cursor) error                   // upsert
	DeleteCursor(ctx context.Context, sessionID string, channelID int64) error
}
