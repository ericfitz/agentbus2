package mcpserver

import (
	"context"

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

// unparkAll clears Parked in the state file of each session of this server.
// A failure is logged and ignored: it must not fail the tool call.
func (s *server) unparkAll() {
	dataDir, err := s.dataDir()
	if err != nil {
		return
	}
	all, err := sessionstate.List(dataDir)
	if err != nil {
		s.log.Warn("listing session state files to unpark failed", "err", err)
		return
	}
	for _, f := range all {
		if !f.Parked || f.MCPPID != s.self {
			continue
		}
		if err := sessionstate.SetParked(dataDir, f.Session, false); err != nil {
			s.log.Warn("unparking a session failed", "session", f.Session, "err", err)
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
