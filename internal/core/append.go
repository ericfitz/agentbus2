package core

import (
	"context"
	"errors"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/core/rules"
	"github.com/ericfitz/agentbus2/internal/store"
)

// AppendInput is one message to append. Tags are already normalized.
type AppendInput struct {
	ChannelID int64
	// Channel is the channel's name, used only to size the envelope.
	Channel string
	// Display is the sender's session display name (<identity>/<hint>-N),
	// used only to size the envelope. Empty means no session resolves, and
	// the wire display falls back to the sender, which EnvelopeSize sizes.
	Display           string
	Kind              store.Kind
	Sender, SessionID string
	// SenderKind is the sender's identity kind (agent or human). Append
	// answers internal for anything else, "" included.
	SenderKind             store.IdentityKind
	Type, Subject, Content string
	ReplyTo                *int64
	Metadata               map[string]string
	Tags                   []string
	// TaskRow marks a row of a tasks-kind channel: it is a memory row but gets
	// no memory_access row (v1 ADR 0013).
	TaskRow bool
}

// EnvelopeSize is the worst-case wire size of the message (v1
// sendEnvelope's size gate) and fails with validation when it exceeds
// max_message_kib. The display name is sized as in.Display, or as the sender
// when none is given.
func (c *Core) EnvelopeSize(in AppendInput, sender string) (int, error) {
	display := in.Display
	if display == "" {
		display = sender
	}
	m := bus.Message{
		Channel: in.Channel, Sender: sender, Session: in.SessionID, Display: display,
		Type: in.Type, Subject: in.Subject, Content: in.Content, ReplyTo: in.ReplyTo,
		Metadata: in.Metadata, Tags: in.Tags,
	}
	size := rules.EnvelopeUpperBound(m, in.Kind != store.KindOrdinary)
	if size > c.Cfg.MaxMessageKiB*1024 {
		return 0, E(bus.CodeValidation, "envelope exceeds max_message_kib (%d KiB)", c.Cfg.MaxMessageKiB)
	}
	return size, nil
}

// Append writes the message inside tx, which must come from Store.Append. On
// a memory-kind (or tasks-kind) channel the row is memory revision 1 of
// itself and, unless TaskRow, gets its memory_access row (v1 insertMessage).
// A reply_to must name an existing message of the same channel (Decision 13);
// when the channel's bridge is live and the parent is itself a reply, the
// stored reply_to is the thread root (Decision 14), and in is not changed.
func (c *Core) Append(ctx context.Context, tx store.Tx, in AppendInput) (int64, *int64, error) {
	if !store.ValidKind(in.Kind) {
		// A programming error; checked before the write so nothing is left behind.
		return 0, nil, E(bus.CodeInternal, "append on unknown channel kind %q", in.Kind)
	}
	if !store.ValidIdentityKind(in.SenderKind) {
		// A programming error: every writer holds the access.Resolve result.
		return 0, nil, E(bus.CodeInternal, "append with sender kind %q", in.SenderKind)
	}
	replyTo := in.ReplyTo
	if replyTo != nil {
		root, err := c.resolveReplyTo(ctx, tx, in.ChannelID, *replyTo)
		if err != nil {
			return 0, nil, err
		}
		replyTo = &root
	}
	now := c.NowMs()
	seq, err := tx.AppendMessage(ctx, store.NewMessage{
		ChannelID: in.ChannelID, Sender: in.Sender, SenderKind: in.SenderKind, SessionID: in.SessionID, CreatedAt: now,
		Type: in.Type, Subject: in.Subject, Content: in.Content, ReplyTo: replyTo,
		Metadata: in.Metadata, Tags: in.Tags,
	})
	if err != nil {
		return 0, nil, FromStore(err)
	}
	if in.Kind == store.KindOrdinary {
		return seq, nil, nil
	}
	if err := tx.SetMemory(ctx, seq, seq, 1); err != nil {
		return 0, nil, FromStore(err)
	}
	if !in.TaskRow {
		if err := tx.TouchMemoryAccess(ctx, []int64{seq}, now); err != nil {
			return 0, nil, FromStore(err)
		}
	}
	id := seq
	return seq, &id, nil
}

// CheckReplyTo applies the reply_to rules without keeping the thread root, so
// a write path can refuse a bad reply_to before inspection.
func (c *Core) CheckReplyTo(ctx context.Context, tx store.Tx, channelID, parent int64) error {
	_, err := c.resolveReplyTo(ctx, tx, channelID, parent)
	return err
}

// resolveReplyTo applies the reply_to rules, in order: the parent exists
// (not_found), it is in channelID (validation; on memory and task channels
// this is the whole memory-record rule), and on a channel whose bridge is live
// a reply parent is replaced by its thread root. The walk follows reply_to
// until a message without one; each hop has a smaller seq, so it ends, and a
// hop whose row is gone stops at the last row found.
func (c *Core) resolveReplyTo(ctx context.Context, tx store.Tx, channelID, parentSeq int64) (int64, error) {
	parent, err := tx.Message(ctx, parentSeq)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, E(bus.CodeNotFound, "reply_to %d does not exist", parentSeq)
		}
		return 0, FromStore(err)
	}
	if parent.ChannelID != channelID {
		return 0, E(bus.CodeValidation, "reply_to %d is in another channel", parentSeq)
	}
	if parent.ReplyTo == nil {
		return parentSeq, nil
	}
	br, err := tx.BridgeByChannel(ctx, channelID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && br.State != store.BridgeLive) {
		return parentSeq, nil
	}
	if err != nil {
		return 0, FromStore(err)
	}
	root := parent
	for root.ReplyTo != nil {
		next, err := tx.Message(ctx, *root.ReplyTo)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return 0, FromStore(err)
		}
		if next.ChannelID != channelID {
			break // a legacy reply into another channel: keep the last row in this one
		}
		root = next
	}
	return root.Seq, nil
}
