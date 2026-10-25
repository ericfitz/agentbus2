package sessions

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/store"
)

// Open opens a session, or resumes one when in.ResumeToken names a retained
// row (Decisions 18, 39, 44, 75, 79). Open takes no idempotency key (pending
// Eric: Milestone 3 item 12): a retried Open leaves a lapsed row that the
// retention sweep removes. State (Definition of Done D): a second Open with
// the same token revives the same row again; concurrent opens of one identity
// run in serializable transactions that read the rows, so they get distinct
// names (or one retries); a crashed client's row lapses and the sweep removes
// it after the retention window.
func (s Service) Open(ctx context.Context, identity string, in bus.OpenSessionInput) (bus.Session, error) {
	if in.Hint != "" {
		if err := store.NameRule(in.Hint); err != nil {
			return bus.Session{}, core.E(bus.CodeValidation, "hint %s", err)
		}
	}
	if in.ResumeToken != "" && !validToken(in.ResumeToken) {
		return bus.Session{}, core.E(bus.CodeValidation, "resume_token is malformed")
	}
	seen := make(map[string]bool, len(in.Cursors))
	for _, oc := range in.Cursors {
		if oc.Channel == "" {
			return bus.Session{}, core.E(bus.CodeValidation, "cursor channel is required")
		}
		if oc.Seq < 0 {
			return bus.Session{}, core.E(bus.CodeValidation, "cursor for %q: seq must not be negative", oc.Channel)
		}
		if seen[oc.Channel] {
			return bus.Session{}, core.E(bus.CodeValidation, "cursor for %q is offered twice", oc.Channel)
		}
		seen[oc.Channel] = true
	}
	// Random values are generated once, before the transaction (Decision 26
	// rule 2); a resume does not use them.
	newID, err := core.RandomHex(16)
	if err != nil {
		return bus.Session{}, bus.Internal(err)
	}
	newToken, err := core.RandomToken(resumeTokenBytes)
	if err != nil {
		return bus.Session{}, bus.Internal(err)
	}
	var out bus.Session
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		out = bus.Session{}
		ident, err := s.RequireIdentity(ctx, tx, identity)
		if err != nil {
			return err
		}
		now := s.NowMs()
		if in.ResumeToken != "" {
			row, err := tx.SessionByResumeTokenHash(ctx, hashToken(in.ResumeToken))
			switch {
			case err == nil:
				return s.resume(ctx, tx, identity, row, in.ResumeToken, now, &out)
			case errors.Is(err, store.ErrNotFound):
				// Swept by the retention job: a fresh session (Decision 18).
			default:
				return core.FromStore(err)
			}
		}
		return s.create(ctx, tx, ident, in, newID, newToken, now, &out)
	})
	if err != nil {
		return bus.Session{}, core.FromStore(err)
	}
	return out, nil
}

// resume revives a retained row: the lease is renewed; cursors, the pending
// batch and the token are untouched.
func (s Service) resume(ctx context.Context, tx store.Tx, identity string, row store.Session, token string, now int64, out *bus.Session) error {
	if row.Identity != identity {
		return core.E(bus.CodeNotFound, "resume token does not name a session of %q", identity)
	}
	if row.ClosedAt != 0 {
		return core.E(bus.CodeSessionClosed, "session %q is closed", row.ID)
	}
	if err := tx.RenewLease(ctx, row.ID, s.LeaseUntil(now)); err != nil {
		return core.FromStore(err)
	}
	cursors, err := tx.Cursors(ctx, row.ID)
	if err != nil {
		return core.FromStore(err)
	}
	pos := make(map[int64]int64, len(cursors))
	for _, c := range cursors {
		pos[c.ChannelID] = c.CursorSeq
	}
	pending, err := s.pending(ctx, tx, identity, pos)
	if err != nil {
		return err
	}
	*out = bus.Session{
		ID: row.ID, Identity: identity, DisplayName: row.DisplayName, ResumeToken: token,
		Resumed: true, Pending: pending,
	}
	return nil
}

// create writes a fresh session with a cursor per subscription (Decision 75)
// and the honored offered cursors (Decision 44).
func (s Service) create(ctx context.Context, tx store.Tx, ident store.Identity, in bus.OpenSessionInput, id, token string, now int64, out *bus.Session) error {
	rows, err := tx.ListSessions(ctx, ident.Name)
	if err != nil {
		return core.FromStore(err)
	}
	var taken []string
	for _, r := range rows {
		if r.ClosedAt == 0 {
			taken = append(taken, r.DisplayName)
		}
	}
	display := DisplayName(ident.Name, in.Hint, taken)
	err = tx.OpenSession(ctx, store.Session{
		ID: id, Identity: ident.Name, DisplayName: display, Hint: in.Hint,
		ResumeTokenHash: hashToken(token), OpenedAt: now, LeaseExpiresAt: s.LeaseUntil(now),
		TagsCursorSeq: ident.TagsAckedSeq,
	})
	if err != nil {
		return core.FromStore(err)
	}
	subs, err := tx.Subscriptions(ctx, ident.Name)
	if err != nil {
		return core.FromStore(err)
	}
	pos := make(map[int64]int64, len(subs))
	for _, sub := range subs {
		pos[sub.ChannelID] = sub.AckedSeq
	}
	rejected, err := s.offered(ctx, tx, ident.Name, in.Cursors, pos)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if err := tx.PutCursor(ctx, store.Cursor{SessionID: id, ChannelID: sub.ChannelID, CursorSeq: pos[sub.ChannelID]}); err != nil {
			return core.FromStore(err)
		}
	}
	pending, err := s.pending(ctx, tx, ident.Name, pos)
	if err != nil {
		return err
	}
	*out = bus.Session{
		ID: id, Identity: ident.Name, DisplayName: display, ResumeToken: token,
		RejectedCursors: rejected, Pending: pending,
	}
	return nil
}

// offered applies the client's offered cursors to pos, the cursor by channel
// id, and returns the rejected ones with their reason (Decision 44).
func (s Service) offered(ctx context.Context, tx store.Tx, identity string, offers []bus.OfferedCursor, pos map[int64]int64) ([]bus.RejectedCursor, error) {
	if len(offers) == 0 {
		return nil, nil
	}
	p, err := access.Resolve(ctx, tx, identity)
	if err != nil {
		return nil, err
	}
	maxSeq, err := tx.MaxSeq(ctx)
	if err != nil {
		return nil, core.FromStore(err)
	}
	var rejected []bus.RejectedCursor
	for _, oc := range offers {
		reject := func(reason string) {
			rejected = append(rejected, bus.RejectedCursor{Channel: oc.Channel, Reason: reason})
		}
		ch, err := tx.ChannelByName(ctx, oc.Channel)
		if errors.Is(err, store.ErrNotFound) {
			reject("unsubscribed")
			continue
		}
		if err != nil {
			return nil, core.FromStore(err)
		}
		if _, subscribed := pos[ch.ID]; !subscribed {
			reject("unsubscribed")
			continue
		}
		if d, err := s.Access.Check(ctx, tx, p, access.Channel{ID: ch.ID}, access.Deliver); err != nil {
			return nil, err
		} else if !d.Allowed {
			reject("unreadable")
			continue
		}
		switch {
		case oc.Seq < ch.EvictedBeforeSeq-1:
			reject("before_retention")
		case oc.Seq > maxSeq:
			reject("ahead")
		default:
			pos[ch.ID] = oc.Seq
		}
	}
	return rejected, nil
}

// pending counts the live messages above each subscribed, readable channel's
// cursor, capped at ReceiveMaxCount. It loads up to that many rows per
// subscription to count them, where v1 used count(*); a CountAfter Store
// method is a later amendment if it shows in profiling.
func (s Service) pending(ctx context.Context, tx store.Tx, identity string, pos map[int64]int64) ([]bus.PendingChannel, error) {
	p, err := access.Resolve(ctx, tx, identity)
	if err != nil {
		return nil, err
	}
	subs, err := tx.Subscriptions(ctx, identity)
	if err != nil {
		return nil, core.FromStore(err)
	}
	var out []bus.PendingChannel
	for _, sub := range subs {
		after, ok := pos[sub.ChannelID]
		if !ok {
			after = sub.AckedSeq
		}
		if d, err := s.Access.Check(ctx, tx, p, access.Channel{ID: sub.ChannelID}, access.Deliver); err != nil {
			return nil, err
		} else if !d.Allowed {
			continue // Receive skips an unreadable subscription too
		}
		ch, err := tx.Channel(ctx, sub.ChannelID)
		if err != nil {
			return nil, core.FromStore(err)
		}
		msgs, err := tx.MessagesAfter(ctx, sub.ChannelID, after, store.MessageFilter{}, s.Cfg.ReceiveMaxCount)
		if err != nil {
			return nil, core.FromStore(err)
		}
		out = append(out, bus.PendingChannel{Channel: ch.Name, Pending: int64(len(msgs))})
	}
	return out, nil
}

// EnsureHinted returns the id of the identity's open session (ClosedAt == 0,
// live or lapsed) with the hint, its lease renewed; when it has none, a new
// session with that hint is opened and its id returned. The resume token is
// generated and discarded: the caller finds the session again by hint, so
// the Slack bridge keeps one session per human (Slack bridge spec 6.3).
// State (Definition of Done D): a second call finds the first call's row;
// two overlapping first calls run in serializable transactions that read the
// rows, so one retries and finds the other's row (or both commit with
// distinct display names, which is harmless); a closed session is replaced.
func (s Service) EnsureHinted(ctx context.Context, identity, hint string) (string, error) {
	if hint == "" {
		return "", core.E(bus.CodeValidation, "hint is required")
	}
	if err := store.NameRule(hint); err != nil {
		return "", core.E(bus.CodeValidation, "hint %s", err)
	}
	newID, err := core.RandomHex(16)
	if err != nil {
		return "", bus.Internal(err)
	}
	newToken, err := core.RandomToken(resumeTokenBytes)
	if err != nil {
		return "", bus.Internal(err)
	}
	var id string
	err = s.Store.Tx(ctx, func(ctx context.Context, tx store.Tx) error {
		id = ""
		ident, err := s.RequireIdentity(ctx, tx, identity)
		if err != nil {
			return err
		}
		rows, err := tx.ListSessions(ctx, identity)
		if err != nil {
			return core.FromStore(err)
		}
		now := s.NowMs()
		for _, r := range rows {
			if r.ClosedAt == 0 && r.Hint == hint {
				if err := tx.RenewLease(ctx, r.ID, s.LeaseUntil(now)); err != nil {
					return core.FromStore(err)
				}
				id = r.ID
				return nil
			}
		}
		var out bus.Session
		if err := s.create(ctx, tx, ident, bus.OpenSessionInput{Hint: hint}, newID, newToken, now, &out); err != nil {
			return err
		}
		id = out.ID
		return nil
	})
	if err != nil {
		return "", core.FromStore(err)
	}
	return id, nil
}
