package cli

import (
	"context"
	"errors"
	"io"

	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// RunHealth implements `agentbus2 health`: the server's health document as
// the caller's tier sees it, one JSON document on stdout (the wire member
// names; members above the tier are omitted). Exit 0 for ok and degraded; a
// bus.Error (unavailable among them) is exit 1.
func RunHealth(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin
	cmd := newOpCmd("health", stderr)
	if _, code, ok := cmd.parse(args, 0, 0); !ok {
		return code
	}
	c, code := cmd.connect(ctx, environ)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()

	h, err := c.Bus.Health(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if err := printJSON(stdout, mcpserver.HealthJSON(h)); err != nil {
		return fail(stderr, errors.New("write: "+err.Error()))
	}
	return 0
}
