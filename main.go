// Command agentbus2 coordinates agents over a local MCP server and an API server.
package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ericfitz/agentbus2/internal/cli"
	"github.com/ericfitz/agentbus2/internal/server/cmd"
)

const usage = cli.Usage

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdout, os.Stderr))
}

// signalContext is canceled by SIGINT or SIGTERM. Once it is canceled the
// signals are released, so a second one takes the default action and exits at
// once instead of being swallowed while the server drains (up to 60 s).
func signalContext() context.Context {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx
}

func run(args []string, environ []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "server" {
		return cmd.Run(signalContext(), args[1:], environ, stdout, stderr)
	}
	return cli.Main(signalContext(), args, environ, os.Stdin, stdout, stderr)
}
