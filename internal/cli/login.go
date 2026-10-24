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
// and one-shot commands read. With -human it runs the browser sign-in of a
// human on a profile with an oidc setting instead (spec 5.3).
func RunLogin(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	fs := newFlagSet("login", stderr)
	o := client.Options{Environ: environ}
	client.Flags(fs, &o)
	human := fs.Bool("human", false, "sign in through the profile's oidc provider (a human)")
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 login: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *human && o.Identity != "" {
		_, _ = fmt.Fprintln(stderr, "agentbus2 login: -identity does not apply with -human: the identity comes from sign-in")
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
	if *human {
		return loginHuman(ctx, o, sel.Profile, stdout, stderr)
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

// loginHuman always runs the browser sign-in (never the cached session) and
// prints who signed in. The profile has been selected and is a server profile.
func loginHuman(ctx context.Context, o client.Options, p profile.Profile, stdout, stderr io.Writer) int {
	if p.OIDC == nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2: login -human needs an oidc setting in profile %q: on a server profile a human signs in through the corporate provider\n", p.Name)
		return 2
	}
	o.HumanSignIn, o.HumanDeferSignIn = true, true
	o.HumanOpenBrowser = testOpenBrowser
	o.HumanAnnounce = func(u string) { _, _ = fmt.Fprintf(stderr, "agentbus2 login: sign in at %s\n", u) }
	c, code := open(ctx, o, stderr)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Human.SignIn(ctx); err != nil {
		return fail(stderr, err)
	}
	s, ok, err := c.Human.Cache.Load()
	if err != nil {
		return fail(stderr, err)
	}
	if !ok {
		return fail(stderr, errors.New("the sign-in succeeded but the session cache holds no entry for it"))
	}
	_, _ = fmt.Fprintf(stdout, "logged in as %s at %s (token expires %s)\n", s.Identity, c.Resolved.Profile.Server, s.ExpiresAt.UTC().Format(time.RFC3339))
	return 0
}
