package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// RunSearch implements `agentbus2 search`. The mode is passed as given: an
// unknown one comes back from the core as validation.
func RunSearch(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("search", stderr)
	mode := c.fs.String("mode", "", "text, semantic or both (default: the server's)")
	channel := c.fs.String("channel", "", "only this channel")
	sender := c.fs.String("sender", "", "only this sender")
	var since, until, thread opInt
	c.fs.Var(&since, "since", "created at or after this time, in ms")
	c.fs.Var(&until, "until", "created before this time, in ms")
	c.fs.Var(&thread, "thread", "only the thread rooted at this seq")
	var tags opList
	c.fs.Var(&tags, "tag", "only messages carrying this tag (repeatable)")
	count := c.fs.Int("count", 0, "maximum hits (0: the server default)")
	cursor := c.fs.String("cursor", "", "the next value of an earlier search")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	res, err := conn.Bus.Search(ctx, bus.SearchInput{Query: rest[0], Mode: *mode, Channel: *channel, Sender: *sender,
		Since: since.v, Until: until.v, Thread: thread.v, Tags: tags, Count: *count, Cursor: *cursor})
	if err != nil {
		return fail(stderr, err)
	}
	if res.Hits == nil {
		res.Hits = []bus.SearchHit{}
	}
	return opPrint(stdout, stderr, res)
}
