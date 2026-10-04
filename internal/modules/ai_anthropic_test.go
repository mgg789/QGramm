//go:build qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAnthropicAdapterToolResults(t *testing.T) {
	c := config.Defaults()
	c.AI.AnthropicKeyEnv = "AI_ANTHROPIC_TEST"
	c.AI.Model = "test-model"
	t.Setenv(c.AI.AnthropicKeyEnv, "test-credential")
	request := func(ctx context.Context, url string, headers map[string]string, body any, private bool) ([]byte, error) {
		if !strings.HasSuffix(url, "/messages") || headers["x-api-key"] != "test-credential" || private {
			t.Fatal("provider transport invalid")
		}
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), `"tool_use_id":"call1"`) {
			t.Fatal("tool result missing")
		}
		return []byte(`{"content":[{"type":"text","text":"answer"}]}`), nil
	}
	a, e := aiAnthropic(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}, {Role: "assistant", Calls: []aiCall{{ID: "call1", Name: "query", Arguments: json.RawMessage(`{}`)}}}, {Role: "tool", ToolCallID: "call1", Content: `{"ok":true}`}}, nil, request)
	if e != nil || a.Text != "answer" {
		t.Fatalf("adapter result %+v %v", a, e)
	}
}
