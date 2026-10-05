//go:build qg_ai_streaming && qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAnthropicStreamTextAndInputJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, "message_start", `{"type":"message_start"}`)
		writeSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		writeSSE(w, "content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-1","name":"query","input":{}}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1}"}}`)
		writeSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()
	c := config.Defaults()
	c.AI.AnthropicURL = server.URL
	c.AI.AnthropicKeyEnv = "AI_STREAM_ANTHROPIC_TEST"
	c.AI.Model = "test-model"
	c.AI.AllowPrivate = true
	t.Setenv(c.AI.AnthropicKeyEnv, "test-credential")
	var events []string
	var eventsMu sync.Mutex
	a, err := aiAnthropicStream(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(kind string, payload json.RawMessage) error {
		eventsMu.Lock()
		defer eventsMu.Unlock()
		events = append(events, kind+":"+string(payload))
		return nil
	})
	if err != nil || a.Text != "hi" || len(a.Calls) != 1 || string(a.Calls[0].Arguments) != `{"x":1}` {
		t.Fatalf("anthropic stream answer %+v, err=%v", a, err)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 4 || events[0] != `text.delta:{"text":"hi"}` {
		t.Fatalf("unexpected anthropic stream events %v", events)
	}
}
