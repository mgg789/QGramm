//go:build qg_mcp && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func init() { aiTools["mcp"] = aiMCPTool; core.Register("mcp", func(*core.Core) error { return nil }) }

// Remote MCP Streamable HTTP only: no stdio, subprocesses, sampling, roots or
// unsolicited server tools. A fresh session bounds authority to one tool call.
func aiMCPTool(ctx context.Context, tool config.Tool, args json.RawMessage, request aiRequester) (string, error) {
	headers := map[string]string{"Accept": "application/json, text/event-stream"}
	if tool.SecretEnv != "" {
		secret := os.Getenv(tool.SecretEnv)
		if secret == "" {
			return "", errors.New("tool credential unavailable")
		}
		headers["Authorization"] = "Bearer " + secret
	}
	data, e := request(ctx, tool.URL, headers, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "QGramm", "version": "1"}}}, tool.AllowPrivate)
	if e != nil {
		return "", e
	}
	var init struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &init) != nil || init.ID != 1 || init.Error != nil || (init.Result.ProtocolVersion != "2025-03-26" && init.Result.ProtocolVersion != "2025-06-18") {
		return "", errors.New("MCP initialization rejected")
	}
	if session := headers[":session"]; session != "" {
		headers["Mcp-Session-Id"] = session
	}
	headers["Mcp-Protocol-Version"] = init.Result.ProtocolVersion
	if _, e = request(ctx, tool.URL, headers, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, tool.AllowPrivate); e != nil {
		return "", e
	}
	data, e = request(ctx, tool.URL, headers, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": tool.Name, "arguments": args}}, tool.AllowPrivate)
	if e != nil {
		return "", e
	}
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &response) != nil || response.ID != 2 || response.Error != nil || len(response.Result) == 0 || len(response.Result) > 65536 {
		return "", errors.New("MCP tool response invalid")
	}
	return string(response.Result), nil
}
