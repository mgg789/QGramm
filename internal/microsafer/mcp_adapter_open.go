//go:build qg_ai_endpoint && qg_e2ee && qg_mcp

package microsafer

import (
	"context"
	"encoding/json"
	"errors"
)

func (r *Runtime) makeMCPHandler(name string, hc HandlerConfig, require bool) (Handler, error) {
	return Handler{Name: name, RequireApproval: require, FnRPC: func(ctx context.Context, req RPCRequest) (json.RawMessage, error) {
		return r.callConfiguredMCP(ctx, req, hc)
	}}, nil
}

func (r *Runtime) callConfiguredMCP(ctx context.Context, req RPCRequest, hc HandlerConfig) (json.RawMessage, error) {
	operation := hc.Operation
	if operation == "" {
		operation = req.Action
	}
	if hc.Operation != "" && req.Action != hc.Operation {
		return nil, errors.New("microsafer: MCP action does not match configured operation")
	}
	out, _, err := r.CallTool(ctx, ToolEndpoint{Name: req.Name, URL: hc.URL, AllowPlaintext: hc.AllowPlaintext}, operation, req.Body)
	return out, err
}
