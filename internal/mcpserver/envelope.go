package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ericfitz/agentbus2/internal/bus"
)

// result marshals v as the tool's text content. A returned error becomes the
// tool's failure text: a *bus.Error already renders as the
// {code,message,retryable} JSON the agent needs, and anything else is wrapped
// as `internal` (ADR 0006), so no other shape reaches the agent from a
// handler.
func result(v any, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, bus.Internal(err)
	}
	j, err := json.Marshal(v)
	if err != nil {
		return nil, nil, bus.Internal(err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(j)}}}, nil, nil
}

// wrapSchemaErrorsInEnvelope normalizes go-sdk's own JSON-schema validation
// failures (a missing required field, a wrong-typed argument) into the same
// {code,message,retryable} JSON envelope every bus.Error produces. AddTool's
// generated handler validates arguments before a tool's own code runs; when
// that fails it sets CallToolResult.IsError with plain SDK text, so without
// this agents would see two error shapes. A result whose text is already a
// JSON object with a "code" member is left untouched.
func wrapSchemaErrorsInEnvelope(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if method != "tools/call" || err != nil {
			return res, err
		}
		ctr, ok := res.(*mcp.CallToolResult)
		if !ok || !ctr.IsError || len(ctr.Content) == 0 {
			return res, err
		}
		tc, ok := ctr.Content[0].(*mcp.TextContent)
		if !ok {
			return res, err
		}
		var probe struct {
			Code string `json:"code"`
		}
		if json.Unmarshal([]byte(tc.Text), &probe) == nil && probe.Code != "" {
			return res, err
		}
		envelope, merr := json.Marshal(map[string]any{
			"code":      string(bus.CodeValidation),
			"message":   bus.TruncateErrorMessage(tc.Text),
			"retryable": false,
		})
		if merr != nil {
			return res, err
		}
		ctr.Content[0] = &mcp.TextContent{Text: string(envelope)}
		return ctr, err
	}
}
