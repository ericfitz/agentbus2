package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// opEditOut is the printed form of bus.EditResult.
type opEditOut struct {
	Seq      int64 `json:"seq"`
	MemoryID int64 `json:"memory_id"`
	Revision int64 `json:"revision"`
	Replaced int64 `json:"replaced"`
}

// RunMemories implements `agentbus2 memories get|edit|delete|revisions`.
func RunMemories(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	subs := map[string]opRunFunc{}
	for _, sub := range []string{"get", "edit", "delete", "revisions"} {
		subs[sub] = func(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
			return runMemories(ctx, sub, args, environ, stdin, stdout, stderr)
		}
	}
	return opDispatch(ctx, "memories", subs, args, environ, stdin, stdout, stderr)
}

func runMemories(ctx context.Context, sub string, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("memories "+sub, stderr)
	fs := c.fs
	var (
		subject, mtype, key     string
		clearSubject, clearTags bool
		tags                    opList
		meta                    opMeta
	)
	switch sub {
	case "edit":
		fs.StringVar(&subject, "subject", "", "new subject (absent keeps it)")
		fs.BoolVar(&clearSubject, "clear-subject", false, "clear the subject")
		fs.StringVar(&mtype, "type", "", "message type")
		fs.Var(&tags, "tag", "tag (repeatable; absent keeps the tags)")
		fs.BoolVar(&clearTags, "clear-tags", false, "remove every tag")
		fs.Var(&meta, "metadata", "metadata k=v (repeatable)")
		fs.StringVar(&key, "key", "", "idempotency key")
	case "delete":
		fs.StringVar(&key, "key", "", "idempotency key")
	}
	maxPos := 1
	if sub == "edit" {
		maxPos = 2 // ID [CONTENT|-]
	}
	pos, code, ok := c.parse(args, 1, maxPos)
	if !ok {
		return code
	}
	id, err := opID(pos[0])
	if err != nil {
		return c.usageErr("%v", err)
	}

	var in bus.EditMemoryInput
	var content *string
	if sub == "edit" {
		if c.seen("subject") && clearSubject {
			return c.usageErr("-subject and -clear-subject are mutually exclusive")
		}
		if len(tags) > 0 && clearTags {
			return c.usageErr("-tag and -clear-tags are mutually exclusive")
		}
		in = bus.EditMemoryInput{ID: id, Type: mtype, Metadata: meta, IdempotencyKey: key}
		switch {
		case clearSubject:
			empty := ""
			in.Subject = &empty
		case c.seen("subject"):
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

	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()

	switch sub {
	case "get":
		m, err := conn.Bus.GetMemory(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, m)
	case "revisions":
		revs, err := conn.Bus.Revisions(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		if revs == nil {
			revs = []bus.Message{}
		}
		return opPrint(stdout, stderr, revs)
	case "delete":
		if err := conn.Bus.Delete(ctx, bus.DeleteMemoryInput{ID: id, IdempotencyKey: key}); err != nil {
			return fail(stderr, err)
		}
		return opPrint(stdout, stderr, map[string]int64{"deleted": id})
	}
	// edit
	if content != nil {
		in.Content = *content
	} else {
		// A metadata-only edit keeps the current content.
		cur, err := conn.Bus.GetMemory(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		in.Content = cur.Content
	}
	r, err := conn.Bus.Edit(ctx, in)
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opEditOut{Seq: r.Seq, MemoryID: r.MemoryID, Revision: r.Revision, Replaced: r.Replaced})
}
