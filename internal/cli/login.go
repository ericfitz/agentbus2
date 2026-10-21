package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/profile"
)

// RunLogin is `agentbus2 login [-profile P] [-identity I]` (Milestone 6
// choice 14): discovery, assertion, token exchange, cache. It always
// exchanges, so it proves the credential works and warms the cache the hooks
// and one-shot commands read.
func RunLogin(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	fs := newFlagSet("login", stderr)
	o := client.Options{Environ: environ}
	client.Flags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 login: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// Check the mode before resolving the identity, so a local profile gets the
	// mode message even when it has no identity.
	sel, err := selectProfile(o)
	if err != nil {
		fail(stderr, err)
		return 2
	}
	if sel.Profile.Mode != profile.ModeServer {
		_, _ = fmt.Fprintln(stderr, "agentbus2: login applies to server profiles only")
		return 2
	}
	c, code := open(ctx, o, stderr)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Source.Refresh(ctx); err != nil {
		return fail(stderr, err)
	}
	e, ok, err := c.Source.Cache.Load(c.Resolved.Identity)
	if err != nil {
		return fail(stderr, err)
	}
	if !ok {
		return fail(stderr, errors.New("the token was minted but the token cache holds no entry for it"))
	}
	_, _ = fmt.Fprintf(stdout, "logged in as %s at %s (token expires %s)\n", c.Resolved.Identity, e.Discovery.Issuer, e.ExpiresAt.UTC().Format(time.RFC3339))
	return 0
}
