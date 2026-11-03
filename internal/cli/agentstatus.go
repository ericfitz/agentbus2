package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/profile"
)

// statusJSON is one status revision in the output of agent-status. last_tag
// is set on list entries only (open note: agent-status 2).
type statusJSON struct {
	Identity    string `json:"identity,omitempty"`
	Revision    int64  `json:"revision"`
	Tag         string `json:"tag"`
	LastTag     string `json:"last_tag,omitempty"`
	Text        string `json:"text"`
	Problem     string `json:"problem,omitempty"`
	Session     string `json:"session"`
	DisplayName string `json:"display_name"`
	SetAt       int64  `json:"set_at"`
}

type statusEntryJSON struct {
	Identity string     `json:"identity"`
	Kind     string     `json:"kind"`
	Live     bool       `json:"live"`
	Status   statusJSON `json:"status"`
}

func toStatusJSON(s bus.AgentStatus, withIdentity bool) statusJSON {
	o := statusJSON{Revision: s.Revision, Tag: string(s.Tag), LastTag: string(s.LastTag), Text: s.Text, Problem: s.Problem,
		Session: s.Session, DisplayName: s.DisplayName, SetAt: s.SetAt}
	if withIdentity {
		o.Identity = s.Identity
	}
	return o
}

// RunAgentStatus is `agentmesh agent-status set|list|history` (issue #10):
// set the calling agent's status, list every visible identity's current
// status, or read one identity's history, oldest first. Each result is one
// JSON document; a bus.Error is exit 1, a flag, arity or session-locate
// error exit 2.
func RunAgentStatus(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "agent-status", map[string]opRunFunc{
		"set": runAgentStatusSet, "list": runAgentStatusList, "history": runAgentStatusHistory,
	}, args, environ, stdin, stdout, stderr)
}

// runAgentStatusSet is `agent-status set [-session ID] [-problem P] [-key K]
// TAG TEXT`. Without -session the harness's state file supplies the session
// and binds the profile and identity (the park pattern; open note:
// agent-status 9); there is never a throwaway session, whose display name
// would be wrong. The tag is validated by the server only.
func runAgentStatusSet(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	cmd := newOpCmd("agent-status set", stderr)
	var session, problem, key string
	cmd.fs.StringVar(&session, "session", "", "session id (default: the session of this harness)")
	cmd.fs.StringVar(&problem, "problem", "", "what is in your way: one line (optional)")
	cmd.fs.StringVar(&key, "key", "", "idempotency key: makes a retry safe")
	rest, code, ok := cmd.parse(args, 2, 2)
	if !ok {
		return code
	}
	dataDir, err := profile.DataDir(environ)
	if err != nil {
		return exitFail(stderr, err)
	}
	l, err := locateSession(dataDir, session, processTable, selfPID())
	if err != nil {
		return exitFail(stderr, err)
	}
	if err := bindProfile(&cmd.o, l); err != nil {
		return exitFail(stderr, err)
	}
	c, code := cmd.connect(ctx, environ)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()

	in := bus.SetAgentStatusInput{Session: l.Session, Tag: bus.StatusTag(rest[0]), Text: rest[1], Problem: problem, IdempotencyKey: key}
	st, err := c.Bus.SetAgentStatus(ctx, in)
	if again, rerr := resumeIfLapsed(ctx, c.Bus, l, err); rerr != nil {
		return fail(stderr, rerr)
	} else if again {
		st, err = c.Bus.SetAgentStatus(ctx, in)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, toStatusJSON(st, true))
}

func runAgentStatusList(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	cmd := newOpCmd("agent-status list", stderr)
	if _, code, ok := cmd.parse(args, 0, 0); !ok {
		return code
	}
	c, code := cmd.connect(ctx, environ)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	es, err := c.Bus.AgentStatuses(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]statusEntryJSON, 0, len(es))
	for _, e := range es {
		out = append(out, statusEntryJSON{Identity: e.Identity, Kind: string(e.Kind), Live: e.Live, Status: toStatusJSON(e.Status, false)})
	}
	return opPrint(stdout, stderr, out)
}

func runAgentStatusHistory(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	cmd := newOpCmd("agent-status history", stderr)
	rest, code, ok := cmd.parse(args, 1, 1)
	if !ok {
		return code
	}
	c, code := cmd.connect(ctx, environ)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	hs, err := c.Bus.AgentStatusHistory(ctx, rest[0])
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]statusJSON, 0, len(hs))
	for _, h := range hs {
		o := toStatusJSON(h, true)
		o.Identity = rest[0] // the identity asked about, even if a row left it empty
		out = append(out, o)
	}
	return opPrint(stdout, stderr, out)
}
