package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/ericfitz/agentbus2/internal/profile"
)

// subagentHookInput is the part of a Claude Code SubagentStart hook's stdin
// that RunSubagentHook reads (v1 ADR 0016).
type subagentHookInput struct {
	Cwd       string `json:"cwd"`
	AgentType string `json:"agent_type"`
}

var slugRun = regexp.MustCompile(`[^a-z0-9]+`)

// subagentName is the hint the hook suggests for the subagent: a slug of its
// agent type with a short random suffix, so two subagents never share a
// display name.
func subagentName(agentType string) string {
	base := strings.Trim(slugRun.ReplaceAllString(strings.ToLower(agentType), "-"), "-")
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	if base == "" {
		base = "subagent"
	}
	var b [2]byte
	_, _ = rand.Read(b[:])
	return base + "-" + hex.EncodeToString(b[:])
}

func subagentHookWork(environ []string, stdin io.Reader) hookResult {
	var hi subagentHookInput
	if err := json.NewDecoder(stdin).Decode(&hi); err != nil {
		return hookResult{stage: "decode", err: err}
	}
	dataDir, err := profile.DataDir(environ)
	if err != nil {
		return hookResult{stage: "locate", err: err}
	}
	// No bus call: the parent's state file is all the hook needs.
	if _, err := locateSession(dataDir, "", processTable, selfPID()); err != nil {
		return hookResult{stage: "locate", err: err}
	}
	text := fmt.Sprintf("Agentbus2: If your dispatcher's prompt asks you to use agentbus2 "+
		"(for example \"use agentbus\" or \"register with agentbus2\"), call the agentbus2 "+
		"register tool with hint=%q, or the hint your prompt names (pick another if it is taken), "+
		"then pass the returned session on every later agentbus2 call. Follow the using-agentbus2 protocol, "+
		"but do not start a background `agentbus2 wait` or `agentbus2 watch`: you are short-lived, "+
		"so use receive instead. Otherwise ignore this note: you do not need to register.",
		subagentName(hi.AgentType))
	b, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":     "SubagentStart",
		"additionalContext": text,
	}})
	if err != nil {
		return hookResult{stage: "decode", err: err}
	}
	return hookResult{out: append(b, '\n')}
}

// RunSubagentHook is the SubagentStart hook (choice 15, v1 ADR 0016): when the
// parent has a session it prints additionalContext telling a subagent to
// register with its own hint only if its prompt asks it to use agentbus2. It
// always exits 0 and falls open within hookBudget.
func RunSubagentHook(ctx context.Context, args []string, environ []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runHook(ctx, environ, stdout, func(context.Context) hookResult {
		return subagentHookWork(environ, stdin)
	})
}
