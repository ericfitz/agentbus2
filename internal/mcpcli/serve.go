package mcpcli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
)

// serveOptions parses `serve`'s flags and builds the server's options. The
// code is -1 to proceed, else the exit code. Options.Client.Local stays nil:
// this binary has no local mode (open note: mcp-only-client 1).
func serveOptions(args, environ []string, stderr io.Writer) (mcpserver.Options, int) {
	fs := newFlagSet("serve", stderr)
	o := client.Options{Environ: environ}
	commonFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return mcpserver.Options{}, parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s serve: unexpected argument %q\n", Binary, fs.Arg(0))
		return mcpserver.Options{}, 2
	}
	applyEnv(&o, environ)
	return mcpserver.Options{Client: o, Edition: mcpserver.EditionMCPOnly}, -1
}

// runServe is `agentbus2-mcponly serve`: the stdio MCP server. The protocol
// owns stdout, so a failure is exit 1 with one line on stderr. A resolution
// failure is left to the tools (choice 11), as `agentbus2 mcp` does.
func runServe(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	opts, code := serveOptions(args, environ, stderr)
	if code != -1 {
		return code
	}
	if err := mcpserver.IdentityGuard(opts.Client, osUserName(), Binary+" serve"); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := mcpserver.Run(ctx, opts); err != nil {
		return fail(stderr, err)
	}
	return 0
}
