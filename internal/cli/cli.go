package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/ericfitz/agentbus2/internal/client"
	"github.com/ericfitz/agentbus2/internal/mcpserver"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Usage is the top-level usage line. `server` is dispatched by package main
// (it is the API server, not a client command); every other name is a case of
// Main.
const Usage = "usage: agentbus2 <init|register|login|mcp|server|watch|wait|stop-hook|subagent-hook|session|channels|send|receive|history|search|memories|tasks|status|version|help> [flags]"

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
	if err := mcpserver.Run(ctx, mcpserver.Options{Client: o}); err != nil {
		return fail(stderr, err)
	}
	return 0
}
