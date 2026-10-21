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
	Channel                string
	Kind                   store.Kind
	Sender, SessionID      string
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
// max_message_kib. The display name is sized as the sender's name.
func (c *Core) EnvelopeSize(in AppendInput, sender string) (int, error) {
	m := bus.Message{
		Channel: in.Channel, Sender: sender, Session: in.SessionID, Display: sender,
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
// A reply_to must name an existing message.
func (c *Core) Append(ctx context.Context, tx store.Tx, in AppendInput) (int64, *int64, error) {
	if in.ReplyTo != nil {
		if _, err := tx.Message(ctx, *in.ReplyTo); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return 0, nil, E(bus.CodeNotFound, "reply_to %d does not exist", *in.ReplyTo)
			}
			return 0, nil, FromStore(err)
		}
	}
	now := c.NowMs()
	seq, err := tx.AppendMessage(ctx, store.NewMessage{
		ChannelID: in.ChannelID, Sender: in.Sender, SessionID: in.SessionID, CreatedAt: now,
		Type: in.Type, Subject: in.Subject, Content: in.Content, ReplyTo: in.ReplyTo,
		Metadata: in.Metadata, Tags: in.Tags,
	})
	if err != nil {
		return 0, nil, FromStore(err)
	}
	switch in.Kind {
	case store.KindOrdinary:
		return seq, nil, nil
	case store.KindMemory, store.KindTasks:
	default:
		return 0, nil, E(bus.CodeInternal, "append on unknown channel kind %q", in.Kind)
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
