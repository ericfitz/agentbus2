package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
)

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
	return opPrint(stdout, stderr, out)
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
