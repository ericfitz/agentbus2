package mcpcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ericfitz/agentmesh/internal/bus"
	"github.com/ericfitz/agentmesh/internal/version"
)

// Binary is the executable's name, the prefix of every message.
const Binary = "agentmesh-mcponly"

const usageLine = "usage: agentmesh-mcponly <serve|register|check|config|help|version> [flags]"

// runFunc is the shape of every command.
type runFunc func(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int

// commands is the dispatch table; help and version are handled in Main.
// open note: mcp-only-client 14: TestOnlyMCPCommands pins the command set.
var commands = map[string]runFunc{
	"serve":    runServe,
	"register": runRegister,
	"check":    runCheck,
	"config":   runConfig,
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
		_, _ = fmt.Fprint(stderr, helpOverview)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		return runHelp(args, stdout, stderr)
	case "version", "-v", "--version":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "%s %s: unexpected argument %q\n%s\n", Binary, args[0], args[1], usageLine)
			return 2
		}
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

// runHelp prints the overview or one topic; more than one topic is exit 2 with
// the usage, and an unknown topic (the empty string included) is exit 2 naming
// the topics.
func runHelp(args []string, stdout, stderr io.Writer) int {
	if len(args) > 2 {
		_, _ = fmt.Fprintf(stderr, "%s %s: unexpected argument %q\n%s\n", Binary, args[0], args[2], usageLine)
		return 2
	}
	topic := ""
	if len(args) == 2 {
		topic = args[1]
	}
	text, ok := Help(topic)
	if ok && len(args) == 2 && topic == "" {
		ok = false
	}
	if !ok {
		_, _ = fmt.Fprintf(stderr, "%s help: unknown topic %q; topics: %s\n", Binary, topic, strings.Join(topics, ", "))
		return 2
	}
	_, _ = fmt.Fprint(stdout, text)
	return 0
}

// newFlagSet is the flag set of command name. Its usage, printed for -h and for
// a flag error, is the flags and then the help topic to read.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(Binary+" "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: %s %s [flags]\n", Binary, name)
		fs.PrintDefaults()
		if topic, ok := seeTopic[name]; ok {
			_, _ = fmt.Fprintf(stderr, "see: %s help %s\n", Binary, topic)
		}
	}
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

// fail prints err as "agentmesh-mcponly: <code>: <message>" for a *bus.Error
// and "agentmesh-mcponly: <err>" otherwise, and returns exit code 1.
func fail(stderr io.Writer, err error) int {
	var be *bus.Error
	if errors.As(err, &be) {
		_, _ = fmt.Fprintf(stderr, "%s: %s: %s\n", Binary, be.Code, be.Message)
	} else {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", Binary, err)
	}
	return 1
}
