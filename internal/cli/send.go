package cli

import (
	"context"
	"io"
	"maps"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// RunSend implements `agentbus2 send`. Content is the argument after the
// channel, or stdin when it is "-" or absent.
func RunSend(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("send", stderr)
	subject := c.fs.String("subject", "", "message subject")
	typ := c.fs.String("type", "", "message type")
	var tags opList
	c.fs.Var(&tags, "tag", "message tag (repeatable)")
	var replyTo opInt
	c.fs.Var(&replyTo, "reply-to", "seq of the message this replies to (it must be in the same channel)")
	var meta opMeta
	c.fs.Var(&meta, "metadata", "metadata k=v (repeatable)")
	session := c.fs.String("session", "", "the sending session id (attribution)")
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 1, 2)
	if !ok {
		return code
	}
	content, err := opReadContent(rest[1:], stdin)
	if err != nil {
		return fail(stderr, err)
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	res, err := conn.Bus.Send(ctx, bus.SendInput{Channel: rest[0], Subject: *subject, Content: content, Type: *typ,
		ReplyTo: replyTo.v, Metadata: maps.Clone(meta), Tags: tags, Session: *session, IdempotencyKey: *key})
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, res)
}
