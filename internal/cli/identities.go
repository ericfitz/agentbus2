package cli

import (
	"context"
	"io"

	"github.com/ericfitz/agentbus2/internal/bus"
)

type opAgentIdentityOut struct {
	Identity        string `json:"identity"`
	Scope           string `json:"scope"`
	Created         bool   `json:"created"`
	EnrollmentToken string `json:"enrollment_token"`
	ExpiresAt       int64  `json:"expires_at"`
}

// RunIdentities implements `agentbus2 identities create [-scope S] NAME`
// (administrator only): the one-time enrollment token it prints is bound to
// NAME and shown once.
func RunIdentities(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return opDispatch(ctx, "identities", map[string]opRunFunc{
		"create": runIdentitiesCreate,
	}, args, environ, stdin, stdout, stderr)
}

func runIdentitiesCreate(ctx context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	c := newOpCmd("identities create", stderr)
	scope := c.fs.String("scope", "", "scope the agent belongs to (default: none)")
	rest, code, ok := c.parse(args, 1, 1)
	if !ok {
		return code
	}
	conn, code := c.connect(ctx, environ)
	if conn == nil {
		return code
	}
	defer func() { _ = conn.Close() }()
	a, err := conn.Bus.CreateAgentIdentity(ctx, bus.CreateAgentIdentityInput{Name: rest[0], Scope: *scope})
	if err != nil {
		return fail(stderr, err)
	}
	return opPrint(stdout, stderr, opAgentIdentityOut{Identity: a.Identity, Scope: a.Scope, Created: a.Created, EnrollmentToken: a.EnrollmentToken, ExpiresAt: a.ExpiresAt})
}
