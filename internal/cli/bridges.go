package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type opBridgeOut struct {
	Channel        string `json:"channel"`
	Scope          string `json:"scope,omitempty"`
	SlackChannelID string `json:"slack_channel_id,omitempty"`
	SlackName      string `json:"slack_name,omitempty"`
	State          string `json:"state"`
	CreatedAt      int64  `json:"created_at"`
	ArchivedAt     int64  `json:"archived_at,omitempty"`
}

type opNotInvitedOut struct {
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

type opBridgeCreatedOut struct {
	opBridgeOut
	NotInvited []opNotInvitedOut `json:"not_invited"`
}

func opBridge(b bus.Bridge) opBridgeOut {
	return opBridgeOut{Channel: b.Channel, Scope: b.Scope, SlackChannelID: b.SlackChannelID, SlackName: b.SlackName,
		State: string(b.State), CreatedAt: b.CreatedAt, ArchivedAt: b.ArchivedAt}
}

// RunBridges implements `agentbus2 bridges create|list|get|remove`
// (administrator only). Where no Slack bridge is wired (local mode, or a
// server started without one) every action is unavailable (not retryable).
func RunBridges(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "bridges", map[string]opRunFunc{
		"create": runBridgesCreate,
		"list":   runBridgesList,
		"get":    runBridgesGet,
		"remove": runBridgesRemove,
	}, args, environ, stdin, stdout, stderr)
}

func runBridgesCreate(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("bridges create", stderr)
	name := c.fs.String("slack-name", "", "name of the new Slack channel (default: derived from the bus channel)")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	res, err := conn.Bus.CreateBridge(ctx, bus.CreateBridgeInput{Channel: rest[0], SlackName: *name})
	if err != nil {
		return fail(stderr, err)
	}
	out := opBridgeCreatedOut{opBridgeOut: opBridge(res.Bridge), NotInvited: make([]opNotInvitedOut, 0, len(res.NotInvited))}
	for _, n := range res.NotInvited {
		out.NotInvited = append(out.NotInvited, opNotInvitedOut{Identity: n.Identity, Reason: n.Reason})
	}
	return opPrint(stdout, stderr, out)
}

func runBridgesList(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("bridges list", stderr)
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	bs, err := conn.Bus.ListBridges(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]opBridgeOut, 0, len(bs))
	for _, b := range bs {
		out = append(out, opBridge(b))
	}
	return opPrint(stdout, stderr, out)
}

func runBridgesGet(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("bridges get", stderr)
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	b, err := conn.Bus.GetBridge(ctx, rest[0])
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opBridge(b))
}

func runBridgesRemove(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("bridges remove", stderr)
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Bus.RemoveBridge(ctx, bus.RemoveBridgeInput{Channel: rest[0]}); err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, struct {
		Channel string `json:"channel"`
		Removed bool   `json:"removed"`
	}{rest[0], true})
}
