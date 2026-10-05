//go:build qg_ai_policy && qg_ai_streaming && qg_openai

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIPolicyOpenAIStreamUsageAndIncludeUsageOption(t *testing.T) {
	previous := aiPolicyActive
	aiPolicyActive = func(context.Context) bool { return true }
	defer func() { aiPolicyActive = previous }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request body")
			return
		}
		options, ok := body["stream_options"].(map[string]any)
		if !ok || options["include_usage"] != true {
			t.Errorf("usage option missing: %#v", body["stream_options"])
		}
		writeSSE(w, "", `{"choices":[{"delta":{"content":"answer"}}]}`)
		writeSSE(w, "", `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		writeSSE(w, "", `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4}}`)
		writeSSE(w, "", "[DONE]")
	}))
	defer server.Close()
	c := config.Defaults()
	c.Features.AIPolicy = true
	c.AI.OpenAIURL = server.URL
	c.AI.OpenAIKeyEnv = "AI_POLICY_OPENAI_STREAM_TEST"
	c.AI.Model = "test-model"
	c.AI.AllowPrivate = true
	t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
	a, err := aiOpenAIStream(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(string, json.RawMessage) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !a.Usage.Known || a.Usage.InputTokens != 12 || a.Usage.OutputTokens != 4 {
		t.Fatalf("unexpected OpenAI stream usage: %+v", a.Usage)
	}
}
