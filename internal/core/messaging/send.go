package messaging

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/access"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// sendFingerprint is the receipt payload of a send: the normalized content
// fields, so a retry with the subject, tags or metadata in another form
// fingerprints the same. The session is attribution of the first send, not
// payload, and is left out: a restarted session of the same identity (a new
// session id) retries a keyed send and gets the stored result (Decision 67).
type sendFingerprint struct {
	Channel  string            `json:"channel"`
	Subject  string            `json:"subject"`
	Content  string            `json:"content"`
	Type     string            `json:"type"`
	ReplyTo  *int64            `json:"reply_to"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
}

// AppendHook runs inside the Store.Append transaction of SendWith, right
// after the message is appended, with its sequence number. An error from it
// rolls the whole send back and is the error SendWith answers (a *bus.Error
// keeps its code). It must not call back into the Store outside tx.
type AppendHook func(ctx context.Context, tx store.Tx, seq int64) error

// Send appends one message (v1 internal/bus/messages.go Send). The rate limit
// is checked inside the transaction and charged once, after the commit; the
// notifier hint and the embedding kick also run after it (Decision 26).
func (s Service) Send(ctx context.Context, identity string, in bus.SendInput) (bus.SendResult, error) {
	return s.SendWith(ctx, identity, in, nil)
}

// SendWith is Send with a hook that runs in the same transaction as the
// append, after it. The transaction may run more than once (a retried
// serialization failure), so the hook must be repeatable. A send replayed
// from a receipt (an idempotency key seen before) appends nothing and runs no
// hook.
func (s Service) SendWith(ctx context.Context, identity string, in bus.SendInput, hook AppendHook) (bus.SendResult, error) {
	var (
		res      bus.SendResult
		replayed bool
		size     int
		chanID   int64
		kind     store.Kind
	)
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		res, replayed, size, chanID, kind = bus.SendResult{}, false, 0, 0, ""
		p, err := access.Resolve(ctx, tx, identity)
		if err != nil {
			return err
		}
		// validateSendShape without the DM branch.
		if in.Channel == "" || in.Content == "" {
			return core.E(bus.CodeValidation, "channel and content are required")
		}
		subject, err := rules.NormalizeSubject(in.Subject)
		if err != nil {
			return err // a rule's *bus.Error, already validation
		}
		tags, err := rules.NormalizeTags(in.Tags)
		if err != nil {
			return err
		}
		metadata := in.Metadata
		if len(metadata) == 0 {
			metadata = nil // nil and {} are the same payload, as for tags
		}
		display := ""
		if in.Session != "" {
			sess, err := tx.Session(ctx, in.Session)
			if errors.Is(err, store.ErrNotFound) || (err == nil && sess.Identity != identity) {
				return core.E(bus.CodeNotFound, "session %q does not exist", in.Session)
			}
			if err != nil {
				return core.FromStore(err)
			}
			switch {
			case sess.ClosedAt != 0:
				return core.E(bus.CodeSessionClosed, "session %q is closed", in.Session)
			case !s.IsLive(sess, s.NowMs()):
				return core.E(bus.CodeSessionLapsed, "session %q has lapsed; resume it", in.Session)
			}
			display = sess.DisplayName
		}
		fp := sendFingerprint{
			Channel: in.Channel, Subject: subject, Content: in.Content, Type: in.Type,
			ReplyTo: in.ReplyTo, Metadata: metadata, Tags: tags,
		}
		replayed, err = s.WithReceipt(ctx, tx, identity, in.IdempotencyKey, fp, &res, func() error {
			ch, err := s.ChannelByName(ctx, tx, in.Channel)
			if err != nil {
				return err
			}
			if ch.Kind == store.KindTasks {
				return core.E(bus.CodeValidation, "%s is a task list; use task_create, task_update, task_claim, task_release", in.Channel)
			}
			if err := s.Access.Require(ctx, tx, p, access.Channel{ID: ch.ID}, access.Write); err != nil {
				return err
			}
			ai := core.AppendInput{
				ChannelID: ch.ID, Channel: ch.Name, Display: display, Kind: ch.Kind,
				Sender: identity, SenderKind: p.Kind, SessionID: in.Session, Type: in.Type, Subject: subject,
				Content: in.Content, ReplyTo: in.ReplyTo, Metadata: metadata, Tags: tags,
			}
			n, err := s.EnvelopeSize(ai, identity)
			if err != nil {
				return err
			}
			if !s.Limits.Check(identity, n, s.Now()) {
				return core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", identity)
			}
			seq, memID, err := s.Append(ctx, tx, ai)
			if err != nil {
				return err
			}
			if hook != nil {
				if err := hook(ctx, tx, seq); err != nil {
					return err
				}
			}
			res = bus.SendResult{Seq: seq, MemoryID: memID}
			size, chanID, kind = n, ch.ID, ch.Kind
			return nil
		})
		return err
	})
	if err != nil {
		return bus.SendResult{}, core.FromStore(err)
	}
	if replayed {
		return res, nil
	}
	s.Limits.Charge(identity, size, s.Now())
	s.Publish(notify.Hint{ChannelID: chanID, Seq: res.Seq})
	if kind == store.KindMemory && s.EmbedSoon != nil {
		s.EmbedSoon()
	}
	return res, nil
}
