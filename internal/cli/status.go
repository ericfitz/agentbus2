package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// RunStatus implements `agentbus2 status`: a human-readable report (the
// shape of v1 internal/cli/status.go with the profile and mode in place of
// config and data). The administrator-only lines are printed when
// Admin.Status succeeds; a forbidden Status is silent. Storage usage is not
// reported: Admin.Usage (Milestone 7 Task 6) is not on this branch.
func RunStatus(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	fs := newFlagSet("status", stderr)
	var o client.Options
	o.Environ = environ
	client.Flags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		return opUsage(stderr, "status takes no arguments, got %q", fs.Arg(0))
	}
	c, code := open(ctx, o, stderr)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()

	where := c.Resolved.Profile.Server
	if c.Resolved.Profile.Mode == profile.ModeLocal {
		where = profile.SQLitePath(c.Resolved.Profile, c.Resolved.DataDir)
	}
	ids, err := c.Bus.Identities(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	chans, err := c.Bus.ListChannels(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	st, serr := c.Bus.Status(ctx)
	if serr != nil && bus.CodeOf(serr) != bus.CodeForbidden {
		return fail(stderr, serr)
	}

	var b []byte
	p := func(format string, a ...any) { b = fmt.Appendf(b, format, a...) }
	p("profile: %s (%s, %s)\n", c.Resolved.Profile.Name, c.Resolved.Profile.Mode, where)
	p("identity: %s\n", c.Resolved.Identity)
	n := 0
	for _, id := range ids {
		n += len(id.Sessions)
	}
	p("sessions (%d):\n", n)
	for _, id := range ids {
		for _, s := range id.Sessions {
			p("  %s (%s) since %s\n", s.DisplayName, s.Identity, time.UnixMilli(s.OpenedAt).UTC().Format(time.RFC3339))
		}
	}
	p("channels (%d):\n", len(chans))
	creators := map[string]string{}
	if serr == nil {
		for _, ci := range st.Channels {
			creators[ci.Name] = ci.Creator
		}
	}
	for _, ch := range chans {
		if cr, ok := creators[ch.Name]; ok {
			p("  %s [%s] created by %s\n", ch.Name, ch.Kind, cr)
		} else {
			p("  %s [%s]\n", ch.Name, ch.Kind)
		}
	}
	if serr == nil {
		for _, nt := range st.Notices {
			p("notice: [%s] %s\n", nt.Kind, nt.Message)
		}
		p("embedding backlog: %d (rejected %d)\n", st.EmbeddingBacklog, st.EmbeddingRejected)
		if st.EmbeddingRejectedLast != "" {
			p("embedding latest rejection: %s\n", st.EmbeddingRejectedLast)
		}
		if st.EmbeddingError != "" {
			p("embedding error at %s: %s\n", time.UnixMilli(st.EmbeddingErrorAt).UTC().Format(time.RFC3339), st.EmbeddingError)
		}
	}
	if _, err := stdout.Write(b); err != nil {
		return fail(stderr, errors.New("write: "+err.Error()))
	}
	return 0
}
