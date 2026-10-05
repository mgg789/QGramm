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
	_, e := c.DB.Exec(`INSERT INTO users(id) VALUES('ai');INSERT INTO devices(id,user_id) VALUES('ai-device','ai');INSERT INTO chats(id,mode) VALUES('chat','basic');INSERT INTO members(chat_id,user_id,role,can_send,joined_seq) VALUES('chat','human','member',1,1),('chat','ai','member',1,1);INSERT INTO messages(id,chat_id,sender,device_id,seq) VALUES('message','chat','human','phone',1);INSERT INTO ai_chats VALUES('chat','ai','ai-device','openai','["query"]',X'00',NULL);INSERT INTO ai_jobs VALUES('job','chat','message','running','',0,0)`)
	if e != nil {
		t.Fatal(e)
	}
	steps, effects := 0, 0
	revokeSource := false
	provider := func(context.Context, config.Config, []aiTurn, []config.Tool, aiRequester) (aiAnswer, error) {
		steps++
		if steps == 1 {
			if revokeSource {
				_, _ = c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='phone'`)
			}
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
	_, _ = c.DB.Exec(`UPDATE ai_chats SET tools='["query"]' WHERE chat_id='chat'`)
	steps, effects, revokeSource = 0, 0, true
	if _, e = aiConversationWith(context.Background(), c, "job", provider, nil, []config.Tool{tool}, request); e == nil || effects != 0 || steps != 1 {
		t.Fatal("revoked source executed external tool")
	}

}
