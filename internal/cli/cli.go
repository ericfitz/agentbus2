package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Usage is the top-level usage: the command line, then OpGrammar. `server` is
// dispatched by package main (it is the API server, not a client command);
// every other name is a case of Main.
const Usage = "usage: agentbus2 <init|register|login|mcp|server|watch|wait|stop-hook|subagent-hook|session|channels|send|receive|history|search|memories|tasks|status|tui|version|help> [flags]\n" + OpGrammar

// runFunc is the shape of every command.
type runFunc func(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int

// commands is the dispatch table of Main: the commands of this package plus
// `mcp`. `help` and `version` are handled in Main itself.
var commands = map[string]runFunc{
	"init":          RunInit,
	"register":      RunRegister,
	"login":         RunLogin,
	"mcp":           runMCP,
	"watch":         RunWatch,
	"wait":          RunWait,
	"stop-hook":     RunStopHook,
	"subagent-hook": RunSubagentHook,
	"session":       RunSession,
	"channels":      RunChannels,
	"send":          RunSend,
	"receive":       RunReceive,
	"history":       RunHistory,
	"search":        RunSearch,
	"memories":      RunMemories,
	"tasks":         RunTasks,
	"status":        RunStatus,
	"tui":           RunTUI,
}

// Main dispatches args[0] (v1 main.go run). An empty or unknown command is
// exit 2 with the usage on stderr; help and version print to stdout and exit 0.
func Main(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, Usage)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, Usage)
		return 0
	case "version", "-v", "--version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	}
	run, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "agentbus2: unknown command %q\n%s\n", args[0], Usage)
		return 2
	}
	return run(ctx, args[1:], environ, stdin, stdout, stderr)
}

// runMCP is `agentbus2 mcp [-profile P] [-identity I]`: the stdio MCP server.
// The protocol owns stdout, so a failure is exit 1 with one line on stderr and
// nothing on stdout; the server's own diagnostics go to its log file.
func runMCP(ctx context.Context, args []string, environ []string, _ io.Reader, _, stderr io.Writer) int {
	fs := newFlagSet("mcp", stderr)
	o := client.Options{Environ: environ}
	client.Flags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agentbus2 mcp: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if err := mcpIdentityGuard(o); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := mcpserver.Run(ctx, mcpserver.Options{Client: o}); err != nil {
		return fail(stderr, err)
	}
	return 0
}

// mcpIdentityGuard is Decision 16: the agent's server never runs as the OS
// login name, which belongs to the human (the TUI's identity). A resolution
// failure is left for mcpserver.Run to report; with no known login name there
// is nothing to compare.
func mcpIdentityGuard(o client.Options) error {
	login := osUserName()
	if login == "" {
		return nil
	}
	r, err := client.Resolve(o)
	if err != nil || r.Identity != login {
		return nil
	}
	return fmt.Errorf("agentbus2 mcp: identity %q is the OS login name, which is the human's; choose another with -identity or the profile's identity (`agentbus2 init -identity NAME` for a repository)", login)
}
