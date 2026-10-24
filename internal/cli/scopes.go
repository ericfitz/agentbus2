package cli

import (
	"context"
	"io"
	"strings"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type opScopeOut struct {
	Name         string   `json:"name"`
	State        string   `json:"state"`
	Retire       string   `json:"retire,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	DoneAt       int64    `json:"done_at,omitempty"`
	LastActivity int64    `json:"last_activity"`
	Channels     []string `json:"channels,omitempty"`
	Identities   []string `json:"identities,omitempty"`
}

func opScope(s bus.Scope) opScopeOut {
	return opScopeOut{Name: s.Name, State: string(s.State), Retire: string(s.Retire), CreatedAt: s.CreatedAt,
		DoneAt: s.DoneAt, LastActivity: s.LastActivity, Channels: s.Channels, Identities: s.Identities}
}

// retireModes are the two values of `scopes done -retire`, in the order the
// usage error names them.
var retireModes = []bus.RetireMode{bus.RetireWithBridge, bus.RetireNormal}

// RunScopes implements `agentbus2 scopes create|list|get|done` (administrator
// only; the Bus judges that).
func RunScopes(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "scopes", map[string]opRunFunc{
		"create": runScopesCreate,
		"list":   runScopesList,
		"get":    runScopesGet,
		"done":   runScopesDone,
	}, args, environ, stdin, stdout, stderr)
}

func runScopesCreate(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("scopes create", stderr)
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
	s, err := conn.Bus.CreateScope(ctx, bus.CreateScopeInput{Name: rest[0], IdempotencyKey: *key})
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opScope(s))
}

func runScopesList(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("scopes list", stderr)
	if _, code, ok := c.parse(args, 0, 0); !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	ss, err := conn.Bus.ListScopes(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	out := make([]opScopeOut, 0, len(ss))
	for _, s := range ss {
		out = append(out, opScope(s))
	}
	return opPrint(stdout, stderr, out)
}

func runScopesGet(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("scopes get", stderr)
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	s, err := conn.Bus.GetScope(ctx, rest[0])
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opScope(s))
}

func runScopesDone(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("scopes done", stderr)
	retire := c.fs.String("retire", "", "how the scope is retired: with_bridge or normal (required)")
	key := c.fs.String("key", "", "idempotency key")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	mode := bus.RetireMode(*retire)
	valid := false
	names := make([]string, 0, len(retireModes))
	for _, m := range retireModes {
		names = append(names, string(m))
		valid = valid || m == mode
	}
	if !valid {
		return c.usageErr("-retire %q: want one of %s", *retire, strings.Join(names, ", "))
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	s, err := conn.Bus.DoneScope(ctx, bus.DoneScopeInput{Name: rest[0], Retire: mode, IdempotencyKey: *key})
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opScope(s))
}
