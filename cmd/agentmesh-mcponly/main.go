// Command agentmesh-mcponly is the MCP-only client: the stdio MCP server and
// `register`, with no local mode, no TUI and no server. The commands are in
// internal/mcpcli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/ericfitz/agentmesh/internal/mcpcli"
)

func main() {
	os.Exit(mcpcli.Main(signalContext(), os.Args[1:], os.Environ(), os.Stdin, os.Stdout, os.Stderr))
}

// signalContext is canceled by SIGINT or SIGTERM. Once it is canceled the
// signals are released, so a second one takes the default action and exits at
// once instead of being swallowed while the server drains.
func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx
}
