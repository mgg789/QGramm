//go:build qg_ai_policy && qg_ai_streaming && qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIPolicyAnthropicStreamCumulativeUsage(t *testing.T) {
	previous := aiPolicyActive
	aiPolicyActive = func(context.Context) bool { return true }
	defer func() { aiPolicyActive = previous }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, "message_start", `{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":4,"cache_read_input_tokens":6}}}`)
		writeSSE(w, "content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`)
		writeSSE(w, "content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`)
		writeSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		writeSSE(w, "message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`)
		writeSSE(w, "message_stop", `{"type":"message_stop"}`)
	}))
	defer server.Close()
	c := config.Defaults()
	c.Features.AIPolicy = true
	c.AI.AnthropicURL = server.URL
	c.AI.AnthropicKeyEnv = "AI_POLICY_ANTHROPIC_STREAM_TEST"
	c.AI.Model = "test-model"
	c.AI.AllowPrivate = true
	t.Setenv(c.AI.AnthropicKeyEnv, "test-credential")
	a, err := aiAnthropicStream(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(string, json.RawMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !a.Usage.Known || a.Usage.InputTokens != 110 || a.Usage.OutputTokens != 7 || a.Usage.CacheReadTokens != 6 || a.Usage.CacheWriteTokens != 4 {
		t.Fatalf("unexpected Anthropic stream usage: %+v", a.Usage)
	}
}
