package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// This file also holds the small grammar helpers of the operator commands of
// Task 9 (session, channels, send, receive, history, search). They carry the
// op prefix so that the commands of the other tasks of the package add their
// own without colliding.

// opCmd is one operator (sub)command: its flag set, with -profile and
// -identity registered, and the client options they fill.
type opCmd struct {
	name   string
	fs     *flag.FlagSet
	o      client.Options
	stderr io.Writer
}

func newOpCmd(name string, stderr io.Writer) *opCmd {
	c := &opCmd{name: name, stderr: stderr}
	c.fs = newFlagSet(name, stderr)
	client.Flags(c.fs, &c.o)
	return c
}

// parse parses args and checks the positional count (maxArgs < 0: unbounded).
// ok is false when the command must return code at once: -h is 0, a flag
// error or a wrong positional count is 2 with the usage on stderr.
func (c *opCmd) parse(args []string, minArgs, maxArgs int) (rest []string, code int, ok bool) {
	if err := c.fs.Parse(args); err != nil {
		return nil, parseExit(err), false
	}
	rest = c.fs.Args()
	if len(rest) < minArgs || (maxArgs >= 0 && len(rest) > maxArgs) {
		_, _ = fmt.Fprintf(c.stderr, "agentbus2 %s: wrong number of arguments (%d)\n", c.name, len(rest))
		c.fs.Usage()
		return nil, 2, false
	}
	return rest, 0, true
}

// usageErr reports a grammar error of the command's own flags: exit 2.
func (c *opCmd) usageErr(format string, a ...any) int {
	_, _ = fmt.Fprintf(c.stderr, "agentbus2 %s: %s\n", c.name, fmt.Sprintf(format, a...))
	return 2
}

// connect builds the Conn; the caller closes it. A nil Conn comes with the
// exit code.
func (c *opCmd) connect(ctx context.Context, environ []string) (*client.Conn, int) {
	c.o.Environ = environ
	return open(ctx, c.o, c.stderr)
}

// seen reports whether the flag was given on the command line.
func (c *opCmd) seen(name string) bool {
	found := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

type opRunFunc func(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int

// opDispatch routes args[0] to a subcommand. No subcommand or an unknown one
// is exit 2 with the usage on stderr; help, -h and --help print it on stdout
// and exit 0.
func opDispatch(ctx context.Context, name string, subs map[string]opRunFunc, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	names := make([]string, 0, len(subs))
	for n := range subs {
		names = append(names, n)
	}
	sort.Strings(names)
	usage := "usage: agentbus2 " + name + " <" + strings.Join(names, "|") + "> [flags]\n"
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	run, ok := subs[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "agentbus2 %s: unknown subcommand %q\n%s", name, args[0], usage)
		return 2
	}
	return run(ctx, args[1:], environ, stdin, stdout, stderr)
}

// opList is a repeatable string flag.
type opList []string

func (l *opList) String() string { return strings.Join(*l, ",") }
func (l *opList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// opMeta is the repeatable -metadata k=v flag. A value without "=", an empty
// key or a repeated key is a grammar error.
type opMeta map[string]string

func (m *opMeta) String() string { return fmt.Sprint(map[string]string(*m)) }
func (m *opMeta) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("metadata %q is not k=v", v)
	}
	if *m == nil {
		*m = opMeta{}
	}
	if _, dup := (*m)[k]; dup {
		return fmt.Errorf("metadata key %q given twice", k)
	}
	(*m)[k] = val
	return nil
}

// opInt is an optional integer flag: nil until given.
type opInt struct{ v *int64 }

func (i *opInt) String() string {
	if i == nil || i.v == nil {
		return ""
	}
	return strconv.FormatInt(*i.v, 10)
}
func (i *opInt) Set(s string) error {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("%q is not an integer", s)
	}
	i.v = &n
	return nil
}

// opCursors is the repeatable -cursor CH=SEQ flag of session open.
type opCursors []bus.OfferedCursor

func (c *opCursors) String() string { return fmt.Sprint(len(*c)) }
func (c *opCursors) Set(v string) error {
	i := strings.LastIndex(v, "=")
	if i <= 0 {
		return fmt.Errorf("cursor %q is not CHANNEL=SEQ", v)
	}
	n, err := strconv.ParseInt(v[i+1:], 10, 64)
	if err != nil {
		return fmt.Errorf("cursor %q: %q is not an integer", v, v[i+1:])
	}
	*c = append(*c, bus.OfferedCursor{Channel: v[:i], Seq: n})
	return nil
}

// opSplitTags splits a comma-separated tag list, trimming space around each
// tag (ported from v1 main.go splitTags).
func opSplitTags(v string) []string {
	parts := strings.Split(v, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

// opACLEntry is the JSON shape of an ACL entry on the command line.
type opACLEntry struct {
	Identity    string   `json:"identity"`
	Permissions []string `json:"permissions"`
}

// opParseACL decodes a JSON array of {identity, permissions}. Anything else,
// an unknown member and trailing data included, is an error. "[]" is an empty
// non-nil list.
func opParseACL(s string) ([]bus.ACLEntry, error) {
	if !strings.HasPrefix(strings.TrimSpace(s), "[") {
		return nil, errors.New("not a JSON array of {identity, permissions}")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var in []opACLEntry
	if err := dec.Decode(&in); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after the JSON array")
	}
	out := make([]bus.ACLEntry, 0, len(in))
	for _, e := range in {
		perms := make([]bus.Permission, len(e.Permissions))
		for i, p := range e.Permissions {
			perms[i] = bus.Permission(p)
		}
		out = append(out, bus.ACLEntry{Identity: e.Identity, Permissions: perms})
	}
	return out, nil
}

// opReadContent returns the message content: the argument, or stdin when it
// is absent or "-".
func opReadContent(rest []string, stdin io.Reader) (string, error) {
	if len(rest) > 0 && rest[0] != "-" {
		return rest[0], nil
	}
	var b bytes.Buffer
	if _, err := io.Copy(&b, stdin); err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return b.String(), nil
}

type opPendingOut struct {
	Channel string `json:"channel"`
	Pending int64  `json:"pending"`
}

type opRejectedOut struct {
	Channel string `json:"channel"`
	Reason  string `json:"reason"`
}

type opSessionOut struct {
	Session         string          `json:"session"`
	DisplayName     string          `json:"display_name"`
	ResumeToken     string          `json:"resume_token"`
	Resumed         bool            `json:"resumed"`
	Pending         []opPendingOut  `json:"pending"`
	RejectedCursors []opRejectedOut `json:"rejected_cursors"`
}

// RunSession implements `agentbus2 session open|close`.
func RunSession(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "session", map[string]opRunFunc{
		"open":  runSessionOpen,
		"close": runSessionClose,
	}, args, environ, stdin, stdout, stderr)
}

func runSessionOpen(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("session open", stderr)
	hint := c.fs.String("hint", "", "session hint (display name suffix)")
	token := c.fs.String("resume-token", "", "resume the session this token belongs to")
	var cursors opCursors
	c.fs.Var(&cursors, "cursor", "offer a cursor, CHANNEL=SEQ (repeatable)")
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	s, err := conn.Bus.Open(ctx, bus.OpenSessionInput{Hint: *hint, ResumeToken: *token, Cursors: slices.Clone(cursors)})
	if err != nil {
		return fail(stderr, err)
	}
	out := opSessionOut{Session: s.ID, DisplayName: s.DisplayName, ResumeToken: s.ResumeToken, Resumed: s.Resumed,
		Pending: []opPendingOut{}, RejectedCursors: []opRejectedOut{}}
	for _, p := range s.Pending {
		out.Pending = append(out.Pending, opPendingOut{Channel: p.Channel, Pending: p.Pending})
	}
	for _, r := range s.RejectedCursors {
		out.RejectedCursors = append(out.RejectedCursors, opRejectedOut{Channel: r.Channel, Reason: r.Reason})
	}
	if err := printJSON(stdout, out); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func runSessionClose(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	c := newOpCmd("session close", stderr)
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Bus.Close(ctx, bus.CloseSessionInput{Session: rest[0], IdempotencyKey: *key}); err != nil {
		return fail(stderr, err)
	}
	return 0
}
