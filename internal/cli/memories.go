package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/client"
)

// opStrings is a repeatable string flag (-tag, -metadata, -blocked-by ...).
type opStrings []string

func (s *opStrings) String() string { return strings.Join(*s, ",") }
func (s *opStrings) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// opUsage is a usage error: it prints through stderr and exits 2.
func opUsage(stderr io.Writer, format string, a ...any) int {
	_, _ = fmt.Fprintf(stderr, "agentbus2: "+format+"\n", a...)
	return 2
}

// opParse parses args against fs allowing flags after positional arguments
// ("edit ID text -tag x"). Everything after a literal "--" is positional.
// It returns the positional arguments, or an error from fs.Parse.
func opParse(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, a := range args {
		if a == "--" {
			tail = args[i+1:]
			args = args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	return append(pos, tail...), nil
}

// opFlagsSet reports which flags of fs were given on the command line.
func opFlagsSet(fs *flag.FlagSet) map[string]bool {
	m := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { m[f.Name] = true })
	return m
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

// opMetadata parses repeated k=v values; nil when none were given.
func opMetadata(vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	m := map[string]string{}
	for _, v := range vals {
		k, val, ok := strings.Cut(v, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("-metadata %q: want k=v", v)
		}
		m[k] = val
	}
	return m, nil
}

// opPrint writes v as JSON; a write failure is a runtime failure.
func opPrint(stdout, stderr io.Writer, v any) int {
	if err := printJSON(stdout, v); err != nil {
		return fail(stderr, errors.New("write: "+err.Error()))
	}
	return 0
}

// opEditOut is the printed form of bus.EditResult.
type opEditOut struct {
	Seq      int64 `json:"seq"`
	MemoryID int64 `json:"memory_id"`
	Revision int64 `json:"revision"`
	Replaced int64 `json:"replaced"`
}

const memoriesUsage = "usage: agentbus2 memories <get|edit|delete|revisions> [flags] ..."

// RunMemories implements `agentbus2 memories`.
func RunMemories(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return opUsage(stderr, "%s", memoriesUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "get", "edit", "delete", "revisions":
	case "-h", "-help", "--help":
		_, _ = fmt.Fprintln(stderr, memoriesUsage)
		return 0
	default:
		return opUsage(stderr, "unknown memories subcommand %q\n%s", sub, memoriesUsage)
	}

	fs := newFlagSet("memories "+sub, stderr)
	var o client.Options
	o.Environ = environ
	client.Flags(fs, &o)
	var (
		subject, mtype, key     string
		clearSubject, clearTags bool
		tags, metaVals          opStrings
	)
	switch sub {
	case "edit":
		fs.StringVar(&subject, "subject", "", "new subject (absent keeps it)")
		fs.BoolVar(&clearSubject, "clear-subject", false, "clear the subject")
		fs.StringVar(&mtype, "type", "", "message type")
		fs.Var(&tags, "tag", "tag (repeatable; absent keeps the tags)")
		fs.BoolVar(&clearTags, "clear-tags", false, "remove every tag")
		fs.Var(&metaVals, "metadata", "metadata k=v (repeatable)")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "delete":
		fs.StringVar(&key, "key", "", "idempotency key")
	}
	pos, err := opParse(fs, args)
	if err != nil {
		return parseExit(err)
	}
	set := opFlagsSet(fs)

	maxPos := 1
	if sub == "edit" {
		maxPos = 2
	}
	if len(pos) < 1 || len(pos) > maxPos {
		return opUsage(stderr, "memories %s: want ID%s, got %d argument(s)", sub, map[bool]string{true: " [CONTENT|-]", false: ""}[sub == "edit"], len(pos))
	}
	id, err := opID(pos[0])
	if err != nil {
		return opUsage(stderr, "%v", err)
	}

	var in bus.EditMemoryInput
	var content *string
	if sub == "edit" {
		if set["subject"] && clearSubject {
			return opUsage(stderr, "-subject and -clear-subject are mutually exclusive")
		}
		if len(tags) > 0 && clearTags {
			return opUsage(stderr, "-tag and -clear-tags are mutually exclusive")
		}
		meta, err := opMetadata(metaVals)
		if err != nil {
			return opUsage(stderr, "%v", err)
		}
		in = bus.EditMemoryInput{ID: id, Type: mtype, Metadata: meta, IdempotencyKey: key}
		switch {
		case clearSubject:
			empty := ""
			in.Subject = &empty
		case set["subject"]:
			in.Subject = &subject
		}
		switch {
		case clearTags:
			in.Tags = []string{}
		case len(tags) > 0:
			in.Tags = []string(tags)
		}
		if len(pos) == 2 {
			text := pos[1]
			if text == "-" {
				b, err := io.ReadAll(stdin)
				if err != nil {
					return fail(stderr, fmt.Errorf("read stdin: %w", err))
				}
				text = string(b)
			}
			content = &text
		}
	}

	c, code := open(ctx, o, stderr)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()

	switch sub {
	case "get":
		m, err := c.Bus.GetMemory(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, m)
	case "revisions":
		revs, err := c.Bus.Revisions(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		if revs == nil {
			revs = []bus.Message{}
		}
		return opPrint(stdout, stderr, revs)
	case "delete":
		if err := c.Bus.Delete(ctx, bus.DeleteMemoryInput{ID: id, IdempotencyKey: key}); err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, map[string]int64{"deleted": id})
	}
	// edit
	if content != nil {
		in.Content = *content
	} else {
		// A metadata-only edit keeps the current content.
		cur, err := c.Bus.GetMemory(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		in.Content = cur.Content
	}
	r, err := c.Bus.Edit(ctx, in)
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opEditOut{Seq: r.Seq, MemoryID: r.MemoryID, Revision: r.Revision, Replaced: r.Replaced})
}
