// Command agentbus2 coordinates agents over a local MCP server and an API server.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ericfitz/agentbus2/internal/server/cmd"
	"github.com/ericfitz/agentbus2/internal/version"
)

const usage = `usage: agentbus2 <command>

commands:
  server    serve the API over SQLite or Postgres (-listen, -sqlite or -postgres)
  version   print the version
  help      print this help`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// signalContext is canceled by SIGINT or SIGTERM.
func signalContext() context.Context {
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, usage)
		return 0
	case "server":
		return cmd.Run(signalContext(), args[1:], stdout, stderr)
	case "version", "-v", "--version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "agentbus2: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
}
