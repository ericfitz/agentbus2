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
	"github.com/ericfitz/agentbus2/internal/store"
)

const adminSubcommands = "users and admins (add, remove, list), reset-credential, create-identity, create-human, enrollment (create, list, revoke)"

const makeAdminReplaced = "make-admin was replaced by: admins add -identity NAME (admins remove -identity NAME)"

const createIdentityUsage = `usage: agentbus2 server admin create-identity NAME

Creates a keyless row for an identity that will register a key later; in open
mode any registrant may bind a key to a keyless name that is not an
administrator, while a keyless administrator can only be claimed with an
enrollment token bound to its name (enrollment create -identity NAME).
`

// hostEnv is what a host command runs against: the identities service and the
// names of the configured issuers (none: every claim-kind add is refused).
type hostEnv struct {
	Svc     identities.Service
	Issuers []string
}

type adminAction func(context.Context, hostEnv, io.Writer) error

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
		_, _ = fmt.Fprintln(stderr, makeAdminReplaced)
		return 2
	case "users", "admins":
		run, code, done = referenceCommand(sub, subArgs, stdout, stderr)
	case "reset-credential":
		run, code, done = resetCredentialCommand(subArgs, stdout, stderr)
	case "create-identity":
		run, code, done = createIdentityCommand(subArgs, stdout, stderr)
	case "create-human":
		run, code, done = createHumanCommand(subArgs, stdout, stderr)
	case "enrollment":
		run, code, done = enrollmentCommand(subArgs, stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin: unknown subcommand %q; subcommands: %s\n", sub, adminSubcommands)
		return 2
	}
	if done {
		return code
	}

	cfg, st, cc, err := openFromConfig(ctx, &f, fs, environ, stderr)
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
	env := hostEnv{Svc: identities.Service{Core: c}}
	for _, is := range cfg.Issuers {
		env.Issuers = append(env.Issuers, is.Name)
	}
	if err := run(ctx, env, stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "agentbus2 server admin %s: %v\n", sub, err)
		return 1
	}
	return 0
}

// referenceCommand parses `users|admins add|remove|list`.
func referenceCommand(list string, args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	use := fmt.Sprintf("usage: agentbus2 server admin %s add|remove|list [-issuer NAME] [-email V | -group V | -subject V | -identity NAME]", list)
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, use)
		return nil, 2, true
	}
	action, rest := args[0], args[1:]
	if action != "add" && action != "remove" && action != "list" {
		_, _ = fmt.Fprintf(stderr, "%s: unknown action %q; %s\n", list, action, use)
		return nil, 2, true
	}
	fs, buf := subFlags(list + " " + action)
	var issuer, email, group, subject, identity *string
	if action != "list" {
		issuer = fs.String("issuer", "", "the configured issuer name (claim kinds)")
		email = fs.String("email", "", "match this email address")
		group = fs.String("group", "", "match members of this group")
		subject = fs.String("subject", "", "match this provider subject")
		identity = fs.String("identity", "", "match this identity name")
	}
	pos, code, done := parseInterleaved(fs, buf, rest, stdout, stderr)
	if done {
		return nil, code, true
	}
	if len(pos) != 0 {
		_, _ = fmt.Fprintf(stderr, "%s %s: unexpected argument %q\n", list, action, pos[0])
		return nil, 2, true
	}
	l := store.RefList(list)
	if action == "list" {
		return func(ctx context.Context, e hostEnv, w io.Writer) error {
			refs, err := e.Svc.ListReferences(ctx, l)
			if err != nil {
				return err
			}
			for _, r := range refs {
				_, _ = fmt.Fprintln(w, formatReference(r))
			}
			return nil
		}, 0, false
	}
	r, err := identities.ParseReference(list, *issuer, *email, *group, *subject, *identity)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s %s: %v\n", list, action, err)
		return nil, 2, true
	}
	if action == "add" {
		return func(ctx context.Context, e hostEnv, w io.Writer) error {
			added, err := e.Svc.AddReference(ctx, r, e.Issuers)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(w, map[bool]string{true: "added", false: "already present"}[added])
			return nil
		}, 0, false
	}
	return func(ctx context.Context, e hostEnv, w io.Writer) error {
		removed, err := e.Svc.RemoveReference(ctx, r)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w, map[bool]string{true: "removed", false: "not present"}[removed])
		return nil
	}, 0, false
}

// formatReference is `{issuer: corp, group: pai-users}` or `{identity: pat}`.
func formatReference(r store.Reference) string {
	if r.Kind == store.RefIdentity {
		return fmt.Sprintf("{identity: %s}", r.Value)
	}
	return fmt.Sprintf("{issuer: %s, %s: %s}", r.Issuer, r.Kind, r.Value)
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
	return func(ctx context.Context, e hostEnv, w io.Writer) error {
		s := e.Svc
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
	return func(ctx context.Context, e hostEnv, w io.Writer) error {
		s := e.Svc
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

func createHumanCommand(args []string, stdout, stderr io.Writer) (adminAction, int, bool) {
	fs, buf := subFlags("create-human")
	pos, code, done := parseInterleaved(fs, buf, args, stdout, stderr)
	if done {
		return nil, code, true
	}
	if len(pos) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: agentbus2 server admin create-human EMAIL")
		return nil, 2, true
	}
	email := pos[0]
	return func(ctx context.Context, e hostEnv, w io.Writer) error {
		name, created, err := e.Svc.CreateIdentityByEmail(ctx, email)
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
		identity := fs.String("identity", "", "bind the token to this name (default: any free name or keyless non-administrator row)")
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
		return func(ctx context.Context, e hostEnv, w io.Writer) error {
			s := e.Svc
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
		return func(ctx context.Context, e hostEnv, w io.Writer) error {
			s := e.Svc
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
		return func(ctx context.Context, e hostEnv, w io.Writer) error {
			s := e.Svc
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
// (`create-identity NAME -h`) and returns the positionals.
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
