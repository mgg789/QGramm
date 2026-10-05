//go:build qg_ai_endpoint && qg_e2ee && qg_openai && qg_mcp

package microsafer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestConfiguredModelAndMCPHandlersRouteThroughDurableMLS(t *testing.T) {
	var modelAuth, toolAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/model" {
			modelAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"delta\":\"ok\"}\n\ndata: [DONE]\n"))
			return
		}
		toolAuth = r.Header.Get("Authorization")
		var in struct {
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Method != "tools/call" {
			http.Error(w, "bad MCP request", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"tool","result":{"ok":true}}`))
	}))
	defer server.Close()
	c := testConfig(t, filepath.Join(t.TempDir(), "state.db"))
	c.Models = map[string]ModelConfig{"m": {URL: server.URL + "/model", Model: "configured", MaxBodyBytes: 1 << 20}}
	c.Handlers = map[string]HandlerConfig{
		"model": {Kind: "llm", Model: "m", RequireApproval: false, ApprovalSet: true},
		"tool":  {Kind: "mcp", URL: server.URL + "/mcp", Operation: "tools/call", RequireApproval: false, ApprovalSet: true},
	}
	s, err := OpenStoreWithKeys(c, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := New("alice", "alice-device")
	if err != nil {
		t.Fatal(err)
	}
	p.attach(s, "chat")
	if _, err = p.MLS.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	if err = p.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(c, s, p, "chat")
	if err = r.RegisterConfiguredHandlers(); err != nil {
		t.Fatal(err)
	}
	modelBody, _ := json.Marshal(ChatCompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	modelResp, err := r.ProcessRPC(context.Background(), RPCRequest{Version: 1, RequestID: "model-request", ClientID: "model-client", Chat: "chat", SourcePeer: "peer", Action: "invoke", Name: "model", Body: modelBody})
	if err != nil || len(modelResp) != 1 || modelResp[0].Kind != "result" || modelResp[0].Seq != 2 {
		t.Fatalf("model handler: %#v %v", modelResp, err)
	}
	toolResp, err := r.ProcessRPC(context.Background(), RPCRequest{Version: 1, RequestID: "tool-request", ClientID: "tool-client", Chat: "chat", SourcePeer: "peer", Action: "tools/call", Name: "tool", Body: json.RawMessage(`{"name":"echo","arguments":{}}`)})
	if err != nil || len(toolResp) != 1 || toolResp[0].Kind != "result" {
		t.Fatalf("MCP handler: %#v %v", toolResp, err)
	}
	if modelAuth != "" || toolAuth != "" {
		t.Fatalf("configured model/tool received relay bearer: model=%q tool=%q", modelAuth, toolAuth)
	}
	items, err := s.PendingOutbox(context.Background())
	if err != nil || len(items) != 4 {
		t.Fatalf("durable handler outbox: %d %v", len(items), err)
	}
}
