package mcpcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/ericfitz/agentbus2/internal/bus"
	"github.com/ericfitz/agentbus2/internal/version"
)

// Binary is the executable's name, the prefix of every message.
const Binary = "agentbus2-mcponly"

const usageLine = "usage: agentbus2-mcponly <serve|register|help|version> [flags]"

// runFunc is the shape of every command.
type runFunc func(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int

// commands is the dispatch table; help and version are handled in Main.
// open note: mcp-only-client 14: TestOnlyMCPCommands pins the command set.
var commands = map[string]runFunc{
	"serve":    runServe,
	"register": runRegister,
}

// commandNames is every command, sorted.
func commandNames() []string {
	names := []string{"help", "version"}
	for n := range commands {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Main dispatches args[0]. An empty or unknown command is exit 2 with the
// usage on stderr; help and version print to stdout and exit 0.
func Main(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, usageLine)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		_, _ = fmt.Fprintln(stdout, usageLine)
		return 0
	case "version", "-v", "--version":
		_, _ = fmt.Fprintln(stdout, version.Version)
		return 0
	}
	run, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "%s: unknown command %q\n%s\n", Binary, args[0], usageLine)
		return 2
	}
	return run(ctx, args[1:], environ, stdin, stdout, stderr)
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(Binary+" "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseExit maps a flag parse error to an exit code: -h is success, anything
// else is a usage error.
func parseExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

// fail prints err as "agentbus2-mcponly: <code>: <message>" for a *bus.Error
// and "agentbus2-mcponly: <err>" otherwise, and returns exit code 1.
func fail(stderr io.Writer, err error) int {
	var be *bus.Error
	if errors.As(err, &be) {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %s\n", Binary, be.Code, be.Message)
	} else {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", Binary, err)
	}
	return 1
}
