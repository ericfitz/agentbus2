package cmd

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ericfitz/agentbus2/internal/core"
	"github.com/ericfitz/agentbus2/internal/core/identities"
)

const adminSubcommands = "make-admin, reset-credential, create-identity, enrollment (create, list, revoke)"

const createIdentityUsage = `usage: agentbus2 server admin create-identity NAME

Creates a keyless row for an identity whose credential is an external issuer
(in open registration mode the server also creates such a row on the first
valid external token); in open mode any registrant may bind a key to a keyless
name that is not an administrator, while a keyless administrator can only be
claimed with an enrollment token.
`

type adminAction func(context.Context, identities.Service, io.Writer) error

// RunAdmin is `agentbus2 server admin`: host commands against the Store with
// no listener and no credential. Exit codes: 0 success or -h, 1 runtime or
// service error, 2 flag, configuration or usage error. listen and registration
// are never consulted. Ported from v1 main.go (run's subcommand dispatch,
// parseExit).
func RunAdmin(ctx context.Context, args []string, environ []string, stdout, stderr io.Writer) int {
	var f flags
	fs := flagSet("agentbus2 server admin", &f, false)
	var usage bytes.Buffer
	fs.SetOutput(&usage)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(&usage, "usage: agentbus2 server admin [flags] SUBCOMMAND [args]\nsubcommands: %s\n", adminSubcommands)
		fs.PrintDefaults()
	}
	if code, done := parseFlags(fs, args, &usage, stdout, stderr); done {
		return code
	}
	rest := fs.Args()
	if len(rest) == 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin: a subcommand is required: %s\n", adminSubcommands)
		return 2
	}
	sub, subArgs := rest[0], rest[1:]

	// Parse the subcommand's own arguments before opening the store, so a
	// usage error never touches it.
	var (
		run  adminAction
		code int
		done bool
	)
	switch sub {
	case "make-admin":
		run, code, done = makeAdminCommand(subArgs, stdout, stderr)
	case "reset-credential":
		run, code, done = resetCredentialCommand(subArgs, stdout, stderr)
	case "create-identity":
		run, code, done = createIdentityCommand(subArgs, stdout, stderr)
	case "enrollment":
		run, code, done = enrollmentCommand(subArgs, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin: unknown subcommand %q; subcommands: %s\n", sub, adminSubcommands)
		return 2
	}
	if done {
		return code
	}

	_, st, cc, err := openFromConfig(ctx, &f, fs, environ, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin: %v\n", err)
		var ce *configError
		if errors.As(err, &ce) {
			return 2
		}
		return 1
	}
	defer func() { _ = st.Close() }()
	c, err := core.New(st, cc, core.Options{})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin: %v\n", err)
		return 1
	}
	if err := run(ctx, identities.Service{Core: c}, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin %s: %v\n", sub, err)
		return 1
	}
	return 0
}

func makeAdminCommand(args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	fs, buf := subFlags("make-admin")
	off := fs.Bool("off", false, "clear the administrator flag instead of setting it")
	pos, code, done := parseInterleaved(fs, buf, args, stdout, stderr)
	if done {
		return nil, code, true
	}
	if len(pos) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: agentbus2 server admin make-admin NAME [-off]")
		return nil, 2, true
	}
	name, admin := pos[0], !*off
	return func(ctx context.Context, s identities.Service, w io.Writer) error {
		if err := s.MakeAdmin(ctx, name, admin); err != nil {
			return err
		}
		if admin {
			_, _ = fmt.Fprintf(w, "%s is now an administrator\n", name)
		} else {
			_, _ = fmt.Fprintf(w, "%s is no longer an administrator\n", name)
		}
		return nil
	}, 0, false
}

func resetCredentialCommand(args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	fs, buf := subFlags("reset-credential")
	pos, code, done := parseInterleaved(fs, buf, args, stdout, stderr)
	if done {
		return nil, code, true
	}
	if len(pos) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: agentbus2 server admin reset-credential NAME")
		return nil, 2, true
	}
	name := pos[0]
	return func(ctx context.Context, s identities.Service, w io.Writer) error {
		n, err := s.ResetCredential(ctx, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "%s: %d credential(s) removed; the name may be registered again\n", name, n)
		return nil
	}, 0, false
}

func createIdentityCommand(args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	fs, buf := subFlags("create-identity")
	pos, code, done := parseInterleaved(fs, buf, args, stdout, stderr)
	if done {
		return nil, code, true
	}
	if len(pos) != 1 {
		_, _ = io.WriteString(stderr, createIdentityUsage)
		return nil, 2, true
	}
	name := pos[0]
	return func(ctx context.Context, s identities.Service, w io.Writer) error {
		created, err := s.CreateIdentity(ctx, name)
		if err != nil {
			return err
		}
		if created {
			_, _ = fmt.Fprintf(w, "%s created\n", name)
		} else {
			_, _ = fmt.Fprintf(w, "%s already exists\n", name)
		}
		return nil
	}, 0, false
}

// enrollmentCommand parses `enrollment create|list|revoke`.
func enrollmentCommand(args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	const use = "usage: agentbus2 server admin enrollment create [-identity NAME] [-expires 24h] [-uses 1] | list | revoke ID"
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, use)
		return nil, 2, true
	}
	action, rest := args[0], args[1:]
	switch action {
	case "create":
		fs, buf := subFlags("enrollment create")
		identity := fs.String("identity", "", "bind the token to this name (default: any free name)")
		expires := fs.Duration("expires", 24*time.Hour, "token lifetime")
		uses := fs.Int("uses", 1, "number of registrations the token allows")
		pos, code, done := parseInterleaved(fs, buf, rest, stdout, stderr)
		if done {
			return nil, code, true
		}
		if len(pos) != 0 {
			_, _ = fmt.Fprintf(stderr, "enrollment create: unexpected argument %q\n", pos[0])
			return nil, 2, true
		}
		if *expires <= 0 || *uses < 1 {
			_, _ = fmt.Fprintln(stderr, "enrollment create: -expires and -uses must be positive")
			return nil, 2, true
		}
		in := identities.TokenInput{Identity: *identity, Expires: *expires, Uses: *uses}
		return func(ctx context.Context, s identities.Service, w io.Writer) error {
			token, row, err := s.CreateEnrollmentToken(ctx, in)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "token: %s\n", token)
			_, _ = fmt.Fprintf(w, "id: %s identity: %s expires: %s uses: %d\n", row.ID, orAny(row.Identity),
				time.UnixMilli(row.ExpiresAt).UTC().Format(time.RFC3339), row.UsesLeft)
			return nil
		}, 0, false
	case "list":
		fs, buf := subFlags("enrollment list")
		pos, code, done := parseInterleaved(fs, buf, rest, stdout, stderr)
		if done {
			return nil, code, true
		}
		if len(pos) != 0 {
			_, _ = fmt.Fprintf(stderr, "enrollment list: unexpected argument %q\n", pos[0])
			return nil, 2, true
		}
		return func(ctx context.Context, s identities.Service, w io.Writer) error {
			rows, err := s.ListEnrollmentTokens(ctx)
			if err != nil {
				return err
			}
			now := s.NowMs()
			for _, r := range rows {
				line := fmt.Sprintf("%s %s %s %d", r.ID, orAny(r.Identity), time.UnixMilli(r.ExpiresAt).UTC().Format(time.RFC3339), r.UsesLeft)
				if r.ExpiresAt <= now {
					line += " expired"
				}
				_, _ = fmt.Fprintln(w, line)
			}
			return nil
		}, 0, false
	case "revoke":
		fs, buf := subFlags("enrollment revoke")
		pos, code, done := parseInterleaved(fs, buf, rest, stdout, stderr)
		if done {
			return nil, code, true
		}
		if len(pos) != 1 {
			_, _ = fmt.Fprintln(stderr, "usage: agentbus2 server admin enrollment revoke ID")
			return nil, 2, true
		}
		id := pos[0]
		return func(ctx context.Context, s identities.Service, w io.Writer) error {
			if err := s.RevokeEnrollmentToken(ctx, id); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(w, "%s revoked\n", id)
			return nil
		}, 0, false
	default:
		_, _ = fmt.Fprintf(stderr, "enrollment: unknown action %q; %s\n", action, use)
		return nil, 2, true
	}
}

func orAny(identity string) string {
	if identity == "" {
		return "any"
	}
	return identity
}

func subFlags(name string) (*flag.FlagSet, *bytes.Buffer) {
	fs := flag.NewFlagSet("agentbus2 server admin "+name, flag.ContinueOnError)
	buf := &bytes.Buffer{}
	fs.SetOutput(buf)
	return fs, buf
}

// parseFlags parses fs; done reports that the caller should return code.
func parseFlags(fs *flag.FlagSet, args []string, usage *bytes.Buffer, stdout, stderr io.Writer) (code int, done bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.Copy(stdout, usage)
			return 0, true
		}
		_, _ = io.Copy(stderr, usage)
		return 2, true
	}
	return 0, false
}

// parseInterleaved parses fs allowing flags after positional arguments
// (`make-admin pat -off`) and returns the positionals.
func parseInterleaved(fs *flag.FlagSet, buf *bytes.Buffer, args []string, stdout, stderr io.Writer) (pos []string, code int, done bool) {
	for {
		if c, d := parseFlags(fs, args, buf, stdout, stderr); d {
			return nil, c, true
		}
		if fs.NArg() == 0 {
			return pos, 0, false
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
