package bus

import (
	"context"
	"time"
)

// OpenSessionInput opens a session, or resumes one when ResumeToken is set
// (Decisions 18, 79). Cursors are positions offered by a client that kept
// them (Decision 44).
type OpenSessionInput struct {
	Hint        string
	ResumeToken string
	Cursors     []OfferedCursor
}

// OfferedCursor is a position a client offers on open.
type OfferedCursor struct {
	Channel string
	Seq     int64
}

// RejectedCursor says why an offered cursor was not adopted. Reason is
// "before_retention", "ahead", "unreadable" or "unsubscribed" (Decision 44).
type RejectedCursor struct{ Channel, Reason string }

// PendingChannel counts the messages waiting on one subscribed channel.
type PendingChannel struct {
	Channel string
	Pending int64
}

// Session is the result of Open (Decisions 15, 39, 40, 44): the session id,
// the display name, the resume token (returned by every resume while the row
// is retained), whether an existing session was resumed, the offered cursors
// that were rejected and the pending counts. Sessions are scoped by id, so
// one identity may drive several.
type Session struct {
	ID              string
	Identity        string
	DisplayName     string
	ResumeToken     string
	Resumed         bool
	RejectedCursors []RejectedCursor
	Pending         []PendingChannel
}

// CloseSessionInput closes a session, freeing its display name.
type CloseSessionInput struct{ Session, IdempotencyKey string }

// ReceiveInput reads the next batch for a session. Ack acknowledges the
// previous batch token.
type ReceiveInput struct {
	Session    string
	Ack        string
	Count      int
	Channels   []string
	IncludeOwn bool
}

// ReceiveResult is one delivery batch (Decision 16): the messages, the batch
// token to ack, whether the batch was redelivered, whether the Ack was
// ignored, retention gaps, a server notice and an instruction.
type ReceiveResult struct {
	Messages    []Message
	Batch       string
	Redelivered bool
	AckIgnored  bool
	Gaps        []Gap
	Notice      string
	Instruction string
}

// WatchInput arms the wake primitive for a session. A Timeout above the
// configured maximum is clamped to it; a negative one is rejected.
type WatchInput struct {
	Session    string
	After      int64
	Timeout    time.Duration
	IncludeOwn bool
}

// WatchResult is the outcome of a watch (Decision 42). Woke false means the
// wait timed out.
type WatchResult struct {
	Woke     bool
	Seq      int64
	Count    int
	Channels []string
}

// SessionInfo describes one live session.
type SessionInfo struct {
	ID          string
	Identity    string
	DisplayName string
	OpenedAt    int64
}

// IdentityInfo describes an identity and its live sessions.
type IdentityInfo struct {
	Name     string
	Admin    bool
	Sessions []SessionInfo
}

// Sessions is the session, delivery and discovery seam.
type Sessions interface {
	Open(ctx context.Context, in OpenSessionInput) (Session, error)
	Close(ctx context.Context, in CloseSessionInput) error
	Receive(ctx context.Context, in ReceiveInput) (ReceiveResult, error)
	Watch(ctx context.Context, in WatchInput) (WatchResult, error)
	// Identities lists identities with a live session (v1 discover).
	Identities(ctx context.Context) ([]IdentityInfo, error)
}
