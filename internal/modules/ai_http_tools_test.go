//go:build qg_http_tools && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIToolLoopExplicitPermissionsAndRevocation(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	tool := config.Tool{Name: "query", Kind: "http", URL: "https://tools.example.com/query", Methods: []string{"POST"}, Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false}}
	_, e := c.DB.Exec(`INSERT INTO ai_chats VALUES('chat','ai','ai-device','openai','["query"]',X'00',NULL);INSERT INTO ai_jobs VALUES('job','chat','message','running','',0,0)`)
	if e != nil {
		t.Fatal(e)
	}
	steps, effects := 0, 0
	provider := func(context.Context, config.Config, []aiTurn, []config.Tool, aiRequester) (aiAnswer, error) {
		steps++
		if steps == 1 {
			return aiAnswer{Calls: []aiCall{{ID: "call", Name: "query", Arguments: json.RawMessage(`{"query":"abc"}`)}}}, nil
		}
		return aiAnswer{Text: "answer"}, nil
	}
	request := func(_ context.Context, url string, headers map[string]string, body any, private bool) ([]byte, error) {
		effects++
		if url != tool.URL || headers[":method"] != "POST" || private {
			t.Fatal("tool authority expanded")
		}
		return []byte(`{"ok":true}`), nil
	}
	answer, e := aiConversationWith(context.Background(), c, "job", provider, []aiTurn{{Role: "user", Content: "hello"}}, []config.Tool{tool}, request)
	if e != nil || answer != "answer" || effects != 1 || steps != 2 {
		t.Fatalf("loop %q %v steps=%d effects=%d", answer, e, steps, effects)
	}
	_, _ = c.DB.Exec(`UPDATE ai_chats SET tools='[]' WHERE chat_id='chat'`)
	steps = 0
	effects = 0
	if _, e = aiConversationWith(context.Background(), c, "job", provider, nil, []config.Tool{tool}, request); e == nil || effects != 0 {
		t.Fatal("revoked tool executed")
	}
}
