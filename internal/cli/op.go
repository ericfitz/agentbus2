package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// The grammar helpers of the operator commands (session, channels, send,
// receive, history, search, memories, tasks, status). They carry the op
// prefix so that the other commands of the package add their own without
// colliding. Every operator command parses through opCmd.parse, so one
// grammar holds across all of them: OpGrammar.

// OpGrammar is the argument grammar of every operator command, printed with
// the usage.
const OpGrammar = `Operator commands take their flags before, between or after the arguments; "--" ends the flags, so an argument that starts with "-" follows it (send general -- "-5 degrees").`

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
	c.fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage of agentbus2 %s:\n", name)
		c.fs.PrintDefaults()
		_, _ = fmt.Fprintln(stderr, OpGrammar)
	}
	client.Flags(c.fs, &c.o)
	return c
}

// parse parses args with flags allowed anywhere (OpGrammar) and checks the
// positional count (maxArgs < 0: unbounded). ok is false when the command
// must return code at once: -h is 0, a flag error or a wrong positional count
// is 2 with the usage on stderr.
func (c *opCmd) parse(args []string, minArgs, maxArgs int) (rest []string, code int, ok bool) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	for {
		if err := c.fs.Parse(args); err != nil {
			return nil, parseExit(err), false
		}
		left := c.fs.Args()
		if len(left) == 0 {
			break
		}
		rest = append(rest, left[0])
		args = left[1:]
	}
	rest = append(rest, tail...)
	if len(rest) < minArgs || (maxArgs >= 0 && len(rest) > maxArgs) {
		want := strconv.Itoa(minArgs)
		switch {
		case maxArgs < 0:
			want += " or more"
		case maxArgs != minArgs:
			want += " to " + strconv.Itoa(maxArgs)
		}
		_, _ = fmt.Fprintf(c.stderr, "agentbus2 %s: want %s argument(s), got %d\n", c.name, want, len(rest))
		c.fs.Usage()
		return nil, 2, false
	}
	return rest, 0, true
}

// usageErr reports a grammar error of the command's own flags or arguments:
// "agentbus2 <command>: ..." on stderr and exit 2.
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
	usage := "usage: agentbus2 " + name + " <" + strings.Join(names, "|") + "> [flags] [arguments]\n" + OpGrammar + "\n"
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

// opPrint writes v as JSON and returns 0; a write failure is a runtime
// failure (exit 1).
func opPrint(stdout, stderr io.Writer, v any) int {
	if err := printJSON(stdout, v); err != nil {
		return fail(stderr, errors.New("write: "+err.Error()))
	}
	return 0
}

// opList is a repeatable string flag (-tag, -channel, -blocked-by ...).
type opList []string

func (l *opList) String() string { return strings.Join(*l, ",") }
func (l *opList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// opMeta is the repeatable -metadata k=v flag: nil until given. A value
// without "=", an empty key or a repeated key is a grammar error.
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

// opID parses a positive numeric id argument.
func opID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q: want a positive integer", s)
	}
	return id, nil
}

// opIDs parses a list of id flag values.
func opIDs(name string, vals []string) ([]int64, error) {
	var out []int64
	for _, v := range vals {
		id, err := opID(v)
		if err != nil {
			return nil, fmt.Errorf("-%s: %w", name, err)
		}
		out = append(out, id)
	}
	return out, nil
}
