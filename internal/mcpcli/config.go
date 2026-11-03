package mcpcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/ericfitz/agentmesh/internal/client"
)

// executable is the path of this binary as the OS reports it; tests replace
// the whole variable. Symlinks are not resolved (Ruling 24): a package
// manager's stable link (Homebrew's bin) survives an upgrade, its versioned
// target does not.
var executable = os.Executable

// shellQuote quotes arg for a POSIX shell: unchanged when every byte is in
// [A-Za-z0-9_@%+=:,./-], else single-quoted with each ' written as '\”.
func shellQuote(arg string) string {
	if arg != "" && !strings.ContainsFunc(arg, needsQuote) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// powershellQuote quotes arg for PowerShell, the default Windows shell:
// unchanged when every character is in [A-Za-z0-9_+=:./\\-] or is an @ that is
// not first, else single-quoted with each single quote doubled (PowerShell
// reads the typographic quotes U+2018 to U+201B as single quotes too). A comma
// (array operator), a percent sign and a leading @ (splatting) therefore force
// quoting. cmd.exe does not read single quotes, so on Windows the command line
// is a PowerShell line (help configure says so).
func powershellQuote(arg string) string {
	safe := arg != ""
	for i, r := range arg {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("_+=:./\\-", r):
		case r == '@' && i > 0:
		default:
			safe = false
		}
	}
	if safe {
		return arg
	}
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range arg {
		if r == '\'' || (r >= '\u2018' && r <= '\u201b') {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// commandLine is exe and args as one line for the shell of goos: PowerShell
// on windows (a quoted program is called with &), POSIX elsewhere.
func commandLine(goos, exe string, args []string) string {
	quote, prefix := shellQuote, ""
	if goos == "windows" {
		quote = powershellQuote
		if q := quote(exe); q != exe {
			prefix = "& "
		}
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, prefix+quote(exe))
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func needsQuote(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	}
	return !strings.ContainsRune("_@%+=:,./-", r)
}

type configServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type configSnippet struct {
	MCPServers map[string]configServer `json:"mcpServers"`
}

// runConfig is `agentmesh-mcponly config [-server URL] [-identity I]
// [-credential FILE] [-profile P]`: the harness's MCP server entry for these
// settings, then the same command as a shell line. It reads the configuration
// and writes and dials nothing, so any number of runs print the same text.
func runConfig(_ context.Context, args []string, environ []string, _ io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("config", stderr)
	o := client.Options{Environ: environ}
	commonFlags(fs, &o)
	if err := fs.Parse(args); err != nil {
		return parseExit(err)
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "%s config: unexpected argument %q\n", Binary, fs.Arg(0))
		return 2
	}
	applyEnv(&o, environ)
	server, code := target("config", o, stderr)
	if code != -1 {
		return code
	}
	if o.Server == "" {
		o.Server = server
	}
	r, err := client.Resolve(o)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s config: %v\n", Binary, err)
		return 2
	}
	exe, err := executable()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s config: cannot find this executable: %v\n", Binary, err)
		return 1
	}
	snippetArgs := []string{"serve"}
	if r.Profile.Name != "default" {
		snippetArgs = append(snippetArgs, "-profile", r.Profile.Name)
	}
	snippetArgs = append(snippetArgs, "-server", server, "-identity", r.Identity)
	if o.Credential != "" {
		snippetArgs = append(snippetArgs, "-credential", o.Credential)
	}
	doc, err := json.MarshalIndent(configSnippet{MCPServers: map[string]configServer{"agentmesh": {Command: exe, Args: snippetArgs}}}, "", "  ")
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n\ncommand line: %s\n", doc, commandLine(runtime.GOOS, exe, snippetArgs))
	return 0
}
