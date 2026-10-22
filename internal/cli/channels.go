package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ericfitz/agentbus2/internal/agentview"
	"github.com/ericfitz/agentbus2/internal/bus"
)

type opChannelOut struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type opChannelInfoOut struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Creator   string `json:"creator"`
	CreatedAt int64  `json:"created_at"`
}

type opACLOut struct {
	Identity    string   `json:"identity"`
	Permissions []string `json:"permissions"`
}

// RunChannels implements `agentbus2 channels list|create|delete|rename|acl|
// subscribe|unsubscribe|tags`. A value the core judges (a kind, a subscribe
// origin, a permission) is passed as given.
func RunChannels(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "channels", map[string]opRunFunc{
		"list":        runChannelsList,
		"create":      runChannelsCreate,
		"delete":      runChannelsDelete,
		"rename":      runChannelsRename,
		"acl":         runChannelsACL,
		"subscribe":   runChannelsSubscribe,
		"unsubscribe": runChannelsUnsubscribe,
		"tags":        runChannelsTags,
	}, args, environ, stdin, stdout, stderr)
}

func runChannelsList(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("channels list", stderr)
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	cs, err := conn.Bus.ListChannels(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]opChannelOut, 0, len(cs))
	for _, ch := range cs {
		out = append(out, opChannelOut{Name: ch.Name, Kind: string(ch.Kind)})
	}
	return opPrint(stdout, stderr, out)
}

func runChannelsCreate(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("channels create", stderr)
	kind := c.fs.String("kind", "", "ordinary, memory or tasks (default ordinary)")
	acl := c.fs.String("acl", "", "ACL as a JSON array of {identity, permissions} (absent: the default ACL; [] : none)")
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	in := bus.CreateChannelInput{Name: rest[0], Kind: bus.Kind(*kind), IdempotencyKey: *key}
	if c.seen("acl") {
		entries, err := opParseACL(*acl)
		if err != nil {
			return c.usageErr("-acl: %v", err)
		}
		in.ACL = entries
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	info, err := conn.Bus.CreateChannel(ctx, in)
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opChannelInfoOut{Name: info.Name, Kind: string(info.Kind), Creator: info.Creator, CreatedAt: info.CreatedAt})
}

// runChannelsDelete keeps the y/N prompt and -y of v1 internal/cli/delete.go.
// The Bus shows an ordinary identity only a channel's name and kind, so the
// prompt names the channel and its kind, not a message count.
func runChannelsDelete(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("channels delete", stderr)
	yes := c.fs.Bool("y", false, "delete without asking")
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	name := rest[0]
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	if !*yes {
		cs, err := conn.Bus.ListChannels(ctx)
		if err != nil {
			return fail(stderr, err)
		}
		kind := ""
		for _, ch := range cs {
			if ch.Name == name {
				kind = string(ch.Kind)
			}
		}
		switch {
		case kind == "" && *key != "":
			// A retry of a keyed delete that already happened: nothing is
			// left to confirm, and the bus replays the first result (or
			// answers not_found when the key was never used).
		case kind == "":
			return fail(stderr, &bus.Error{Code: bus.CodeNotFound, Message: fmt.Sprintf("channel %q does not exist", name)})
		default:
			if code := confirmDelete(name, kind, stdin, stdout, stderr); code != 0 {
				return code
			}
		}
	}
	if err := conn.Bus.DeleteChannel(ctx, bus.DeleteChannelInput{Name: name, IdempotencyKey: *key}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// confirmDelete asks the y/N question of v1 internal/cli/delete.go; anything
// but y or yes cancels (exit 1).
func confirmDelete(name, kind string, stdin io.Reader, stdout, stderr io.Writer) int {
	if _, err := fmt.Fprintf(stdout, "Channel %q (%s) and everything in it will be deleted.\nDeleting it PERMANENTLY destroys its messages. There is no undo.\nDelete channel %q? [y/N] ", name, kind, name); err != nil {
		return fail(stderr, err)
	}
	line, readErr := bufio.NewReader(stdin).ReadString('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fail(stderr, readErr)
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		return fail(stderr, errors.New("delete cancelled"))
	}
	return 0
}

func runChannelsRename(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	c := newOpCmd("channels rename", stderr)
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 2, 2)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Bus.RenameChannel(ctx, bus.RenameChannelInput{Name: rest[0], NewName: rest[1], IdempotencyKey: *key}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func runChannelsACL(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("channels acl", stderr)
	set := c.fs.String("set", "", "replace the ACL with this JSON array of {identity, permissions}")
	key := c.fs.String("key", "", "idempotency key (with -set)")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	var entries []bus.ACLEntry
	setting := c.seen("set")
	if setting {
		var err error
		if entries, err = opParseACL(*set); err != nil {
			return c.usageErr("-set: %v", err)
		}
	} else if c.seen("key") {
		return c.usageErr("-key applies to -set only")
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	if setting {
		if err := conn.Bus.SetACL(ctx, bus.SetACLInput{Channel: rest[0], Entries: entries, IdempotencyKey: *key}); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	got, err := conn.Bus.ACL(ctx, rest[0])
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]opACLOut, 0, len(got))
	for _, en := range got {
		perms := make([]string, 0, len(en.Permissions))
		for _, p := range en.Permissions {
			perms = append(perms, string(p))
		}
		out = append(out, opACLOut{Identity: en.Identity, Permissions: perms})
	}
	return opPrint(stdout, stderr, out)
}

func runChannelsSubscribe(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	c := newOpCmd("channels subscribe", stderr)
	from := c.fs.String("from", "", "now or oldest (default now)")
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
	if mem, err := agentview.NewKindIndex().IsMemory(ctx, conn.Bus, rest[0]); err != nil {
		return fail(stderr, err)
	} else if mem {
		return fail(stderr, bus.Errorf(bus.CodeValidation, false, "%s is a memory channel: agents do not subscribe to memories (decision 20); use search to find a memory and get_memory to read it", rest[0]))
	}
	if err := conn.Bus.Subscribe(ctx, bus.SubscribeInput{Channel: rest[0], From: *from, IdempotencyKey: *key}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func runChannelsUnsubscribe(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	c := newOpCmd("channels unsubscribe", stderr)
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
	if err := conn.Bus.Unsubscribe(ctx, bus.UnsubscribeInput{Channel: rest[0], IdempotencyKey: *key}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func runChannelsTags(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("channels tags", stderr)
	sub := c.fs.String("subscribe", "", "add this comma-separated tag set (AND)")
	unsub := c.fs.String("unsubscribe", "", "remove this comma-separated tag set")
	key := c.fs.String("key", "", "idempotency key (with -subscribe or -unsubscribe)")
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	hasSub, hasUnsub := c.seen("subscribe"), c.seen("unsubscribe")
	switch {
	case hasSub && hasUnsub:
		return c.usageErr("-subscribe and -unsubscribe are exclusive")
	case !hasSub && !hasUnsub && c.seen("key"):
		return c.usageErr("-key applies to -subscribe and -unsubscribe only")
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	switch {
	case hasSub:
		if err := conn.Bus.SubscribeTags(ctx, bus.TagSubscribeInput{Patterns: opSplitTags(*sub), IdempotencyKey: *key}); err != nil {
			return fail(stderr, err)
		}
		return 0
	case hasUnsub:
		if err := conn.Bus.UnsubscribeTags(ctx, bus.TagSubscribeInput{Patterns: opSplitTags(*unsub), IdempotencyKey: *key}); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	sets, err := conn.Bus.TagSubscriptions(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if sets == nil {
		sets = [][]string{}
	}
	return opPrint(stdout, stderr, sets)
}
