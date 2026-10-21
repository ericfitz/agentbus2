package cli

import (
	"context"
	"errors"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type opGapOut struct {
	Channel string `json:"channel"`
	From    int64  `json:"from"`
	To      int64  `json:"to"`
}

type opReceiveOut struct {
	Messages    []bus.Message `json:"messages"`
	Batch       string        `json:"batch,omitempty"`
	Redelivered bool          `json:"redelivered"`
	AckIgnored  bool          `json:"ack_ignored"`
	Gaps        []opGapOut    `json:"gaps,omitempty"`
	Notice      string        `json:"notice,omitempty"`
	Instruction string        `json:"instruction,omitempty"`
}

// RunReceive implements `agentbus2 receive`. Without -session it opens a
// session with hint "cli", receives, acks with a second receive unless
// -no-ack, and closes the session; with -session it touches nothing but that
// session and acks only what -ack names.
func RunReceive(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("receive", stderr)
	session := c.fs.String("session", "", "receive on this session (default: a throwaway session)")
	ack := c.fs.String("ack", "", "batch token to acknowledge (with -session)")
	count := c.fs.Int("count", 0, "maximum messages (0: the server default)")
	var channels opList
	c.fs.Var(&channels, "channel", "only this channel (repeatable)")
	includeOwn := c.fs.Bool("include-own", false, "include the session's own messages")
	noAck := c.fs.Bool("no-ack", false, "do not acknowledge the batch (throwaway session only)")
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	if *session == "" && *ack != "" {
		return c.usageErr("-ack needs -session: a throwaway session has no earlier batch")
	}
	if *ack != "" && *noAck {
		return c.usageErr("-ack and -no-ack are mutually exclusive")
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()

	id := *session
	throwaway := id == ""
	if throwaway {
		s, err := conn.Bus.Open(ctx, bus.OpenSessionInput{Hint: "cli"})
		if err != nil {
			return fail(stderr, err)
		}
		id = s.ID
	}
	code = receiveOnce(ctx, conn.Bus, stdout, stderr, bus.ReceiveInput{Session: id, Ack: *ack, Count: *count, Channels: channels, IncludeOwn: *includeOwn}, throwaway && !*noAck)
	if throwaway {
		// The close must happen even when the command was cancelled.
		err := conn.Bus.Close(context.WithoutCancel(ctx), bus.CloseSessionInput{Session: id})
		if err != nil && code == 0 {
			code = fail(stderr, err)
		}
	}
	return code
}

// receiveOnce reads one batch, prints it and, when ackAfter, acknowledges it
// with a second receive whose own batch is discarded (the throwaway session
// closes right after, so that batch is delivered again to the next reader).
func receiveOnce(ctx context.Context, b bus.Bus, stdout, stderr io.Writer, in bus.ReceiveInput, ackAfter bool) int {
	res, err := b.Receive(ctx, in)
	if err != nil {
		return fail(stderr, err)
	}
	out := opReceiveOut{Messages: res.Messages, Batch: res.Batch, Redelivered: res.Redelivered, AckIgnored: res.AckIgnored,
		Notice: res.Notice, Instruction: res.Instruction}
	if out.Messages == nil {
		out.Messages = []bus.Message{}
	}
	for _, g := range res.Gaps {
		out.Gaps = append(out.Gaps, opGapOut{Channel: g.Channel, From: g.From, To: g.To})
	}
	if code := opPrint(stdout, stderr, out); code != 0 {
		return code
	}
	if ackAfter && res.Batch != "" {
		in.Ack = res.Batch
		next, err := b.Receive(ctx, in)
		if err != nil {
			return fail(stderr, err)
		}
		if next.AckIgnored {
			return fail(stderr, errors.New("the server ignored the acknowledgement"))
		}
	}
	return 0
}
