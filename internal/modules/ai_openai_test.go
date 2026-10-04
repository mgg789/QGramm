//go:build qg_openai

package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestOpenAIAdapterToolRequestAndResponse(t *testing.T) {
	c := config.Defaults()
	c.AI.OpenAIKeyEnv = "AI_ADAPTER_TEST"
	c.AI.Model = "test-model"
	t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
	request := func(ctx context.Context, url string, headers map[string]string, body any, private bool) ([]byte, error) {
		if !strings.HasSuffix(url, "/chat/completions") || headers["Authorization"] != "Bearer test-credential" || private {
			t.Fatal("provider transport invalid")
		}
		raw, _ := json.Marshal(body)
		if !strings.Contains(string(raw), `"tools"`) {
			t.Fatal("tool definitions missing")
		}
		return []byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call1","function":{"name":"query","arguments":"{\"query\":\"abc\"}"}}]}}]}`), nil
	}
	a, e := aiOpenAI(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, []config.Tool{{Name: "query", Schema: map[string]any{"type": "object"}}}, request)
	if e != nil || len(a.Calls) != 1 || a.Calls[0].Name != "query" {
		t.Fatalf("adapter result %+v %v", a, e)
	}
}
