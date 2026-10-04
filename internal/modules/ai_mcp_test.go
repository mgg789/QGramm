//go:build qg_mcp && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestMCPStreamableHTTPInitializeSessionAndCall(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid RPC request")
		}
		calls = append(calls, req.Method)
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "test-session")
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{}}}}`))
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "test-session" {
				t.Error("session missing")
			}
			w.WriteHeader(202)
		case "tools/call":
			if r.Header.Get("Mcp-Protocol-Version") != "2025-03-26" {
				t.Error("protocol missing")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n"))
		default:
			t.Error("unexpected method")
		}
	}))
	defer server.Close()
	result, e := aiMCPTool(context.Background(), config.Tool{Name: "query", Kind: "mcp", URL: server.URL, AllowPrivate: true}, json.RawMessage(`{}`), aiRequest)
	if e != nil || len(result) == 0 || len(calls) != 3 {
		t.Fatalf("MCP result %q %v %v", result, e, calls)
	}
}
