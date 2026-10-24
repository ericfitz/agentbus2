package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// RunHistory implements `agentbus2 history`.
func RunHistory(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("history", stderr)
	var before, after, thread opInt
	c.fs.Var(&before, "before", "messages with seq below this")
	c.fs.Var(&after, "after", "messages with seq above this")
	c.fs.Var(&thread, "thread", "only the thread rooted at this seq: that message and the messages replying to it (complete for replies stored after the channel's Slack bridge went live; an earlier reply may point at another reply)")
	count := c.fs.Int("count", 0, "maximum messages (0: the server default)")
	var tags opList
	c.fs.Var(&tags, "tag", "only messages carrying this tag (repeatable)")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	ms, err := conn.Bus.History(ctx, bus.HistoryInput{Channel: rest[0], Before: before.v, After: after.v, Count: *count, Tags: tags, Thread: thread.v})
	if err != nil {
		return fail(stderr, err)
	}
	if ms == nil {
		ms = []bus.Message{}
	}
	return opPrint(stdout, stderr, ms)
}
