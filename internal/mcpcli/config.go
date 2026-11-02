package mcpcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ericfitz/agentbus2/internal/client"
)

// executable is the path of this binary; tests replace the whole variable.
var executable = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// shellQuote quotes arg for a POSIX shell: unchanged when every byte is in
// [A-Za-z0-9_@%+=:,./-], else single-quoted with each ' written as '\”.
func shellQuote(arg string) string {
	if arg != "" && !strings.ContainsFunc(arg, needsQuote) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
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

// runConfig is `agentbus2-mcponly config [-server URL] [-identity I]
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
	doc, err := json.MarshalIndent(configSnippet{MCPServers: map[string]configServer{"agentbus2": {Command: exe, Args: snippetArgs}}}, "", "  ")
	if err != nil {
		return fail(stderr, err)
	}
	quoted := make([]string, len(snippetArgs))
	for i, a := range snippetArgs {
		quoted[i] = shellQuote(a)
	}
	_, _ = fmt.Fprintf(stdout, "%s\n\ncommand line: %s %s\n", doc, shellQuote(exe), strings.Join(quoted, " "))
	return 0
}
