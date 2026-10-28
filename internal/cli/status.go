package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// RunStatus implements `agentbus2 status`: a human-readable report (the
// shape of v1 internal/cli/status.go with the profile and mode in place of
// config and data). The administrator-only lines (notices, embedding and
// storage usage) are printed when Admin.Status succeeds; a forbidden Status or
// Usage prints nothing. The usage line names the budget when the store is
// evictable, then the disk size, the ratio and the pressure tier.
func RunStatus(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	cmd := newOpCmd("status", stderr)
	if _, code, ok := cmd.parse(args, 0, 0); !ok {
		return code
	}
	c, code := cmd.connect(ctx, environ)
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
	var usage bus.Usage
	uerr := serr
	if serr == nil {
		usage, uerr = c.Bus.Usage(ctx)
		if uerr != nil && bus.CodeOf(uerr) != bus.CodeForbidden {
			return fail(stderr, uerr)
		}
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
		row := fmt.Sprintf("  %s [%s]", ch.Name, ch.Kind)
		if cr, ok := creators[ch.Name]; ok {
			row += " created by " + cr
		}
		if ch.Has(bus.ChannelBridged) {
			row += " bridged"
		}
		p("%s\n", row)
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
	if uerr == nil {
		p("%s\n", usageLine(usage))
	}
	if _, err := stdout.Write(b); err != nil {
		return fail(stderr, errors.New("write: "+err.Error()))
	}
	return 0
}

// usageLine is v1 internal/cli/status.go's "usage: %.1f MiB of %d MiB budget",
// without the budget when the store is not evictable, then the disk size, the
// ratio of disk to bytes and the pressure tier.
func usageLine(u bus.Usage) string {
	line := fmt.Sprintf("usage: %.1f MiB", float64(u.Bytes)/(1<<20))
	if u.Evictable {
		line += fmt.Sprintf(" of %d MiB budget", u.BudgetBytes>>20)
	}
	line += fmt.Sprintf(" (disk %.1f MiB, %.2fx, %s)", float64(u.DiskBytes)/(1<<20), u.DiskRatio, u.Tier)
	return line
}
