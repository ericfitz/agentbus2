package messaging

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/notify"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// sendFingerprint is the receipt payload of a send: the normalized fields,
// so a retry with the subject or tags in another form fingerprints the same.
type sendFingerprint struct {
	Channel  string            `json:"channel"`
	Subject  string            `json:"subject"`
	Content  string            `json:"content"`
	Type     string            `json:"type"`
	ReplyTo  *int64            `json:"reply_to"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
	Session  string            `json:"session"`
}

// Send appends one message (v1 internal/bus/messages.go Send). The rate limit
// is checked inside the transaction and charged once, after the commit; the
// notifier hint and the embedding kick also run after it (Decision 26).
func (s Service) Send(ctx context.Context, identity string, in bus.SendInput) (bus.SendResult, error) {
	var (
		res      bus.SendResult
		replayed bool
		size     int
		chanID   int64
		kind     store.Kind
	)
	err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
		res, replayed, size, chanID, kind = bus.SendResult{}, false, 0, 0, ""
		if _, err := s.RequireIdentity(ctx, tx, identity); err != nil {
			return err
		}
		// validateSendShape without the DM branch.
		if in.Channel == "" || in.Content == "" {
			return core.E(bus.CodeValidation, "channel and content are required")
		}
		subject, err := rules.NormalizeSubject(in.Subject)
		if err != nil {
			return core.E(bus.CodeValidation, "%s", err.Error())
		}
		tags, err := rules.NormalizeTags(in.Tags)
		if err != nil {
			return core.E(bus.CodeValidation, "%s", err.Error())
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
			ReplyTo: in.ReplyTo, Metadata: in.Metadata, Tags: tags, Session: in.Session,
		}
		replayed, err = s.WithReceipt(ctx, tx, identity, in.IdempotencyKey, fp, &res, func() error {
			ch, err := s.ChannelByName(ctx, tx, in.Channel)
			if err != nil {
				return err
			}
			if ch.Kind == store.KindTasks {
				return core.E(bus.CodeValidation, "%s is a task list; use task_create, task_update, task_claim, task_release", in.Channel)
			}
			if err := s.Authorize(ctx, tx, identity, ch.ID, store.PermWrite); err != nil {
				return err
			}
			ai := core.AppendInput{
				ChannelID: ch.ID, Channel: ch.Name, Display: display, Kind: ch.Kind,
				Sender: identity, SessionID: in.Session, Type: in.Type, Subject: subject,
				Content: in.Content, ReplyTo: in.ReplyTo, Metadata: in.Metadata, Tags: tags,
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
