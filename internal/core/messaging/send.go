package messaging

import (
	"context"
	"errors"
	"fmt"

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

// SendOptions are the optional parts of SendWith.
type SendOptions struct {
	// Hook runs in the append transaction, after the append (see AppendHook).
	Hook AppendHook
	// Source is where the write came from: "" is core.SourceAPI; anything but
	// api and slack is an internal error.
	Source core.Source
}

// sendPlan is what both passes of a send agree on: the principal, the
// normalized shape, the session's display name and the receipt fingerprint.
// Building it writes nothing (Decision 26).
type sendPlan struct {
	p       access.Principal
	in      bus.SendInput
	subject string
	tags    []string
	meta    map[string]string
	display string
	fp      sendFingerprint
}

// planSend is the side-effect-free front of a send, shared by the read
// pre-pass and the Store.Append pass.
func (s Service) planSend(ctx context.Context, tx store.Tx, identity string, in bus.SendInput) (sendPlan, error) {
	p, err := access.Resolve(ctx, tx, identity)
	if err != nil {
		return sendPlan{}, err
	}
	// validateSendShape without the DM branch.
	if in.Channel == "" || in.Content == "" {
		return sendPlan{}, core.E(bus.CodeValidation, "channel and content are required")
	}
	subject, err := rules.NormalizeSubject(in.Subject)
	if err != nil {
		return sendPlan{}, err // a rule's *bus.Error, already validation
	}
	tags, err := rules.NormalizeTags(in.Tags)
	if err != nil {
		return sendPlan{}, err
	}
	metadata := in.Metadata
	if len(metadata) == 0 {
		metadata = nil // nil and {} are the same payload, as for tags
	}
	display := ""
	if in.Session != "" {
		if err := s.Access.Require(ctx, tx, p, access.Session{ID: in.Session}, access.Use); err != nil {
			return sendPlan{}, err
		}
		sess, err := tx.Session(ctx, in.Session)
		if err != nil {
			return sendPlan{}, core.FromStore(err)
		}
		switch {
		case sess.ClosedAt != 0:
			return sendPlan{}, core.E(bus.CodeSessionClosed, "session %q is closed", in.Session)
		case !s.IsLive(sess, s.NowMs()):
			return sendPlan{}, core.E(bus.CodeSessionLapsed, "session %q has lapsed; resume it", in.Session)
		}
		display = sess.DisplayName
	}
	return sendPlan{
		p: p, in: in, subject: subject, tags: tags, meta: metadata, display: display,
		fp: sendFingerprint{
			Channel: in.Channel, Subject: subject, Content: in.Content, Type: in.Type,
			ReplyTo: in.ReplyTo, Metadata: metadata, Tags: tags,
		},
	}, nil
}

// target resolves the channel and runs every refusal that precedes the
// append: write access, the task-list refusal, the reply_to rule, the size
// and the rate check. It returns the append input and the envelope size.
func (s Service) target(ctx context.Context, tx store.Tx, pl sendPlan) (core.AppendInput, int, error) {
	ch, err := s.ChannelByName(ctx, tx, pl.in.Channel)
	if err != nil {
		return core.AppendInput{}, 0, err
	}
	if err := s.Access.Require(ctx, tx, pl.p, access.Channel{ID: ch.ID}, access.Write); err != nil {
		return core.AppendInput{}, 0, err
	}
	if ch.Kind == store.KindTasks {
		return core.AppendInput{}, 0, core.E(bus.CodeValidation, "%s is a task list; use task_create, task_update, task_claim, task_release", pl.in.Channel)
	}
	if pl.in.ReplyTo != nil {
		// Refused before inspection, as the append would.
		if err := s.CheckReplyTo(ctx, tx, ch.ID, *pl.in.ReplyTo); err != nil {
			return core.AppendInput{}, 0, err
		}
	}
	ai := core.AppendInput{
		ChannelID: ch.ID, Channel: ch.Name, Display: pl.display, Kind: ch.Kind,
		Sender: pl.p.Name, SenderKind: pl.p.Kind, SessionID: pl.in.Session, Type: pl.in.Type, Subject: pl.subject,
		Content: pl.in.Content, ReplyTo: pl.in.ReplyTo, Metadata: pl.meta, Tags: pl.tags,
	}
	n, err := s.EnvelopeSize(ai, pl.p.Name)
	if err != nil {
		return core.AppendInput{}, 0, err
	}
	if !s.Limits.Check(pl.p.Name, n, s.Now()) {
		return core.AppendInput{}, 0, core.E(bus.CodeRateLimited, "send_messages_per_second rate limit exceeded for %s", pl.p.Name)
	}
	return ai, n, nil
}

// sendPrepared is the inspection request for a send. It carries the sender
// kind and channel kind as the Store holds them, never compared (ADR 0011).
func sendPrepared(pl sendPlan, ai core.AppendInput, size int, src core.Source) core.Prepared {
	return core.Prepared{
		Inspect: true,
		Request: core.InspectRequest{
			Operation: core.OpSend, Source: src, Sender: pl.p.Name, SenderKind: string(pl.p.Kind),
			Session: pl.display,
			Channel: core.InspectChannel{Name: ai.Channel, Kind: string(ai.Kind)},
			Record: core.InspectRecord{
				Subject: pl.subject, Content: pl.in.Content, Type: pl.in.Type, Tags: pl.tags,
				Metadata: pl.meta, ReplyTo: pl.in.ReplyTo,
			},
		},
		ChannelID: ai.ChannelID,
		Size:      size,
	}
}

// Send appends one message (v1 internal/bus/messages.go Send). The rate limit
// is checked inside the transaction and charged once, after the commit; the
// notifier hint and the embedding kick also run after it (Decision 26).
func (s Service) Send(ctx context.Context, identity string, in bus.SendInput) (bus.SendResult, error) {
	return s.SendWith(ctx, identity, in, SendOptions{})
}

// SendWith is Send with a hook that runs in the same transaction as the
// append, after it, and a Source. The transaction may run more than once (a
// retried serialization failure), so the hook must be repeatable. A send
// replayed from a receipt (an idempotency key seen before) appends nothing and
// runs no hook. With an inspector configured the send runs under write
// inspection (core.RunInspected): a read pre-pass, the inspector, then this
// append with the digest the inspector judged.
func (s Service) SendWith(ctx context.Context, identity string, in bus.SendInput, opts SendOptions) (bus.SendResult, error) {
	src := opts.Source
	switch src {
	case "":
		src = core.SourceAPI
	case core.SourceAPI, core.SourceSlack:
	default:
		return bus.SendResult{}, bus.Internal(fmt.Errorf("messaging: unknown send source %q", src))
	}
	var (
		res      bus.SendResult
		replayed bool
		size     int
		chanID   int64
		kind     store.Kind
	)
	prepare := func(ctx context.Context, tx store.Tx) (core.Prepared, error) {
		pl, err := s.planSend(ctx, tx, identity, in)
		if err != nil {
			return core.Prepared{}, err
		}
		var r bus.SendResult
		hit, _, err := s.CheckReceipt(ctx, tx, identity, in.IdempotencyKey, pl.fp, &r)
		if err != nil || hit {
			return core.Prepared{}, err // a replay needs no inspection
		}
		ai, n, err := s.target(ctx, tx, pl)
		if err != nil {
			return core.Prepared{}, err
		}
		return sendPrepared(pl, ai, n, src), nil
	}
	write := func(want string) error {
		err := s.Store.Append(ctx, func(ctx context.Context, tx store.Tx) error {
			res, replayed, size, chanID, kind = bus.SendResult{}, false, 0, 0, ""
			pl, err := s.planSend(ctx, tx, identity, in)
			if err != nil {
				return err
			}
			replayed, err = s.WithReceipt(ctx, tx, identity, in.IdempotencyKey, pl.fp, &res, func() error {
				ai, n, err := s.target(ctx, tx, pl)
				if err != nil {
					return err
				}
				if want != "" && sendPrepared(pl, ai, n, src).Digest() != want {
					return core.ErrInspectedChanged
				}
				seq, memID, err := s.Append(ctx, tx, ai)
				if err != nil {
					return err
				}
				if opts.Hook != nil {
					if err := opts.Hook(ctx, tx, seq); err != nil {
						return err
					}
				}
				res = bus.SendResult{Seq: seq, MemoryID: memID}
				size, chanID, kind = n, ai.ChannelID, ai.Kind
				return nil
			})
			return err
		})
		if errors.Is(err, core.ErrInspectedChanged) {
			return err
		}
		return core.FromStore(err)
	}
	if err := s.RunInspected(ctx, identity, prepare, write); err != nil {
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
