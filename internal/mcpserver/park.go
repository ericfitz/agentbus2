package mcpserver

import (
	"context"
	"errors"
	"io/fs"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/sessionstate"
)

// unparkOnToolCall is decision 18: any tool call other than receive (register
// clears the mark itself, in its own state file write) unparks every session
// this server holds. It runs before the tool, so a call that fails still
// counts: the agent is working again. The state files are the source of
// truth, because `agentbus2 park` writes them behind the server's back.
func (s *server) unparkOnToolCall(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "tools/call" {
			if r, ok := req.(*mcp.CallToolRequest); ok && r.Params != nil && r.Params.Name != "receive" && r.Params.Name != "register" {
				s.unparkAll()
			}
		}
		return next(ctx, method, req)
	}
}

// unparkAll clears Parked in the state file of each session of this server
// and of this harness (see parkTargets). A failure is logged and ignored: it
// must not fail the tool call.
func (s *server) unparkAll() {
	dataDir, err := s.dataDir()
	if err != nil {
		return
	}
	s.smu.Lock()
	ids := s.registryIDsLocked()
	s.smu.Unlock()
	s.unparkSessions(dataDir, ids)
}

// registryIDsLocked lists the sessions this server holds; s.smu is held.
func (s *server) registryIDsLocked() []string {
	ids := make([]string, 0, len(s.files))
	for id := range s.files {
		ids = append(ids, id)
	}
	return ids
}

// unparkSessions clears Parked for ids and for every state file of this
// server's harness: after an MCP server restart the files keep the dead
// server's pid and are adopted by id only, and a hinted register must unpark
// the harness's primary session too. Missing files are ignored.
func (s *server) unparkSessions(dataDir string, ids []string) {
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if s.harness.Pid > 0 {
		all, err := sessionstate.List(dataDir)
		if err != nil {
			s.log.Warn("listing session state files to unpark failed", "err", err)
		}
		for _, f := range all {
			if f.Parked && f.HarnessPID == s.harness.Pid && (s.harness.Start == 0 || f.HarnessStart == 0 || f.HarnessStart == s.harness.Start) {
				seen[f.Session] = true
			}
		}
	}
	for id := range seen {
		if err := sessionstate.SetParked(dataDir, id, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("unparking a session failed", "session", id, "err", err)
		}
	}
}

// keepParked copies the on-disk Parked mark into f: the registry's copy of a
// state file is stale after `agentbus2 park`, and a rewrite from it must not
// erase the park.
func keepParked(dataDir string, f *sessionstate.File) {
	if d, err := sessionstate.Read(dataDir, f.Session); err == nil {
		f.Parked = d.Parked
	}
}
