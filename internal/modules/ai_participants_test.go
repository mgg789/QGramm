//go:build qg_openai || qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func configureNamedBotFixture(c *core.Core, provider string) {
	allowPrivate := true
	c.Config.AI.Endpoints = map[string]config.AIEndpoint{
		"named-test": {
			Provider:     provider,
			URL:          "http://127.0.0.1:9000/v1",
			Model:        "named-test-model",
			Auth:         "none",
			AllowPrivate: &allowPrivate,
		},
	}
	c.Config.AI.Bots = map[string]config.AIBot{
		"ai-one": {Endpoint: "named-test", Tools: []string{}},
	}
	c.Config.AI.DefaultBot = "ai-one"
}

func addNamedAgentFixture(t *testing.T, c *core.Core, agent string) {
	t.Helper()
	now := time.Now().Unix()
	if _, err := c.DB.Exec(`INSERT INTO users(id) VALUES(?)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO devices(id,user_id,public_key,signing_key) VALUES(?,?,?,?)`, agent+"-device", agent, "pub", "sign"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO ai_agents(id,bot_name,user_id,device_id,provider,profile,tools,version,private_key,signing_key,created_at,updated_at) VALUES(?,?,?,?,?,X'00','[]',1,X'00',X'00',?,?)`, agent, agent, agent, agent+"-device", "openai", now, now); err != nil {
		t.Fatal(err)
	}
}

func addNamedChatFixture(t *testing.T, c *core.Core, chat, agent string) {
	t.Helper()
	if _, err := c.DB.Exec(`INSERT INTO chats(id,kind,mode,seq,epoch,pending,created_at) VALUES(?,'group','basic',0,0,0,?)`, chat, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES(?,?, 'owner',1,0,1)`, chat, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES(?,'human','member',1,0,1)`, chat); err != nil {
		t.Fatal(err)
	}
	ctx, err := c.Engine.Seal([]byte("[]"), []byte("ai/context/"+chat+"/"+agent+"/1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO ai_sessions(chat_id,agent_id,context,tools,version,state,created_at,updated_at) VALUES(?,?,?,'[]',1,X'',?,?)`, chat, agent, ctx, time.Now().Unix(), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

func addNamedMessageFixture(t *testing.T, c *core.Core, chat, id string) {
	t.Helper()
	payload, err := c.Engine.Seal([]byte("question"), []byte("message/"+id))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,revision,deleted,payload,metadata,epoch,created_at) VALUES(?,?, 'human','phone',?,?,1,0,?,'{}',0,?)`, id, chat, "op-"+id, 1, payload, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
}

func TestNamedClaimsSerializeOneSession(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	addNamedAgentFixture(t, c, "ai-one")
	addNamedChatFixture(t, c, "named-chat", "ai-one")
	addNamedMessageFixture(t, c, "named-chat", "named-message")
	now := time.Now().Unix()
	for i := 0; i < 2; i++ {
		message := "named-message"
		if i == 1 {
			message = "named-message-2"
			addNamedMessageFixture(t, c, "named-chat", message)
		}
		if _, err := c.DB.Exec(`INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES(?,?,?,?,'queued','',?,?)`, uuid.NewString(), "named-chat", "ai-one", message, now, now); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	claims := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimed, err := claimAINamedTask(context.Background(), c)
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			claims <- claimed
		}()
	}
	wg.Wait()
	close(claims)
	count := 0
	for claimed := range claims {
		if claimed {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claimed %d tasks for one session", count)
	}
	var running int
	if err := c.DB.QueryRow(`SELECT count(*) FROM ai_tasks WHERE status='running'`).Scan(&running); err != nil || running != 1 {
		t.Fatalf("running tasks=%d err=%v", running, err)
	}
}

func TestNamedTaskTargetsAreAtomicAndRespectACL(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	addNamedAgentFixture(t, c, "ai-one")
	addNamedAgentFixture(t, c, "ai-two")
	addNamedChatFixture(t, c, "target-chat", "ai-one")
	if _, err := c.DB.Exec(`INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES('target-chat','ai-two','member',1,0,1)`); err != nil {
		t.Fatal(err)
	}
	ctx, err := c.Engine.Seal([]byte("[]"), []byte("ai/context/target-chat/ai-two/1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO ai_sessions(chat_id,agent_id,context,tools,version,state,created_at,updated_at) VALUES('target-chat','ai-two',?,'[]',1,X'',0,0)`, ctx); err != nil {
		t.Fatal(err)
	}
	addNamedMessageFixture(t, c, "target-chat", "target-message")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/chats/target-chat/ai/tasks", nil)
	r.SetPathValue("chat", "target-chat")
	body := httptest.NewRequest(http.MethodPost, "/", jsonBody(t, map[string]any{"message_id": "target-message", "agents": []string{"ai-one", "ai-two"}}))
	body.SetPathValue("chat", "target-chat")
	aiQueueNamedTasks(c, w, body, core.Identity{UserID: "human", DeviceID: "phone"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("queue status=%d body=%s", w.Code, w.Body)
	}
	var queued int
	if err := c.DB.QueryRow(`SELECT count(*) FROM ai_tasks WHERE message_id='target-message'`).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
	if _, err := c.DB.Exec(`UPDATE members SET active=0 WHERE chat_id='target-chat' AND user_id='ai-two'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	body = httptest.NewRequest(http.MethodPost, "/", jsonBody(t, map[string]any{"message_id": "target-message", "agents": []string{"ai-two"}}))
	body.SetPathValue("chat", "target-chat")
	aiQueueNamedTasks(c, w, body, core.Identity{UserID: "human", DeviceID: "phone"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("inactive target status=%d", w.Code)
	}
}

func TestNamedDeleteAndRestartFailClosed(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	addNamedAgentFixture(t, c, "ai-one")
	addNamedChatFixture(t, c, "delete-chat", "ai-one")
	addNamedMessageFixture(t, c, "delete-chat", "delete-message")
	if _, err := c.DB.Exec(`INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES('delete-task','delete-chat','ai-one','delete-message','running','',0,0)`); err != nil {
		t.Fatal(err)
	}
	tx, err := c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.OnDelete[0](context.Background(), tx, "delete-message"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id='delete-task'`).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("delete status=%q err=%v", status, err)
	}
	if _, err = c.DB.Exec(`UPDATE ai_tasks SET status='running' WHERE id='delete-task'`); err != nil {
		t.Fatal(err)
	}
	restarted := &core.Core{DB: c.DB, Config: c.Config, Engine: c.Engine, Mux: http.NewServeMux(), Context: context.Background()}
	if err = installAINamed(restarted); err != nil {
		t.Fatal(err)
	}
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id='delete-task'`).Scan(&status); err != nil || status != "uncertain" {
		t.Fatalf("restart status=%q err=%v", status, err)
	}
}

func TestNamedEnqueueRejectsPreJoinSourceAndFinalRevocation(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	addNamedAgentFixture(t, c, "ai-one")
	addNamedChatFixture(t, c, "history-chat", "ai-one")
	addNamedMessageFixture(t, c, "history-chat", "history-message")
	if _, err := c.DB.Exec(`UPDATE members SET joined_seq=2 WHERE chat_id='history-chat' AND user_id='human'`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", jsonBody(t, map[string]any{"message_id": "history-message", "agents": []string{"ai-one"}}))
	r.SetPathValue("chat", "history-chat")
	aiQueueNamedTasks(c, w, r, core.Identity{UserID: "human", DeviceID: "phone"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("pre-join enqueue status=%d body=%s", w.Code, w.Body)
	}
	if _, err := c.DB.Exec(`UPDATE members SET joined_seq=0 WHERE chat_id='history-chat' AND user_id='human'; INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES('late-task','history-chat','ai-one','history-message','running','',0,0); UPDATE devices SET revoked=1 WHERE id='phone'`); err != nil {
		t.Fatal(err)
	}
	identity := aiTaskIdentity{Chat: "history-chat", User: "ai-one", Device: "ai-one-device", SourceUser: "human", SourceDevice: "phone", SourceMessage: "history-message", Mode: "basic", Epoch: 0}
	err := aiCompleteNamed(context.Background(), c, aiNamedTask{ID: "late-task", ChatID: "history-chat", AgentID: "ai-one", MessageID: "history-message"}, identity, 1, nil, "late answer")
	if err == nil {
		t.Fatal("revoked source device allowed final output")
	}
}

func TestNamedCurrentTOMLToolRemovalFailsClosed(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	previous, hadPrevious := aiTools["named-test"]
	aiTools["named-test"] = func(context.Context, config.Tool, json.RawMessage, aiRequester) (string, error) { return "", nil }
	defer func() {
		if hadPrevious {
			aiTools["named-test"] = previous
		} else {
			delete(aiTools, "named-test")
		}
	}()
	settings := c.Config.AI
	settings.Tools = []config.Tool{{Name: "lookup", Kind: "named-test"}}
	if _, err := aiNamedToolsCurrent(c, settings, []string{"lookup"}, []string{"lookup"}); err != nil {
		t.Fatalf("configured tool rejected: %v", err)
	}
	settings.Tools = nil
	if _, err := aiNamedToolsCurrent(c, settings, []string{"lookup"}, []string{"lookup"}); err == nil {
		t.Fatal("removed TOML tool remained executable")
	}
	encoded, err := json.Marshal(aiNamedTask{ID: "id", ChatID: "chat", MessageID: "message", AgentID: "agent", Status: "queued", ResultID: "", CreatedAt: 1, UpdatedAt: 2})
	if err != nil || !strings.Contains(string(encoded), `"chat_id":"chat"`) || strings.Contains(string(encoded), `"ChatID"`) {
		t.Fatalf("task JSON contract invalid: %s", encoded)
	}
}

func TestNamedSchemaUnknownVersionFailsClosed(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	if _, err := c.DB.Exec(`INSERT INTO ai_named_schema_versions(version) VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	restarted := &core.Core{DB: c.DB, Config: c.Config, Engine: c.Engine, Mux: http.NewServeMux(), Context: context.Background()}
	if err := installAINamed(restarted); err == nil {
		t.Fatal("unknown named schema version was accepted")
	}
}

func TestNamedFinalContextRejectsOversizedLatestTurn(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	addNamedAgentFixture(t, c, "ai-one")
	addNamedChatFixture(t, c, "oversized-chat", "ai-one")
	addNamedMessageFixture(t, c, "oversized-chat", "oversized-message")
	if _, err := c.DB.Exec(`INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES('oversized-task','oversized-chat','ai-one','oversized-message','running','',0,0)`); err != nil {
		t.Fatal(err)
	}
	c.Config.AI.MaxContextBytes = 32
	identity := aiTaskIdentity{Chat: "oversized-chat", User: "ai-one", Device: "ai-one-device", SourceUser: "human", SourceDevice: "phone", SourceMessage: "oversized-message", Mode: "basic", Epoch: 0}
	err := aiCompleteNamed(context.Background(), c, aiNamedTask{ID: "oversized-task", ChatID: "oversized-chat", AgentID: "ai-one", MessageID: "oversized-message"}, identity, 1, nil, strings.Repeat("x", 128))
	if err == nil {
		t.Fatal("oversized final context was accepted")
	}
	var status string
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id='oversized-task'`).Scan(&status); err != nil || status != "running" {
		t.Fatalf("oversized context changed task status=%q err=%v", status, err)
	}
}

func TestNamedFailedOrCancelledProviderDoesNotPersistPrompt(t *testing.T) {
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	for _, tc := range []struct {
		name   string
		cancel bool
	}{
		{name: "provider-error"},
		{name: "provider-cancel", cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, cleanup := aiFixture(t)
			defer cleanup()
			configureNamedBotFixture(c, provider)
			addNamedAgentFixture(t, c, "ai-one")
			addNamedChatFixture(t, c, "failed-context-chat", "ai-one")
			addNamedMessageFixture(t, c, "failed-context-chat", "failed-message")
			if _, err := c.DB.Exec(`INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES('failed-task','failed-context-chat','ai-one','failed-message','queued','',0,0)`); err != nil {
				t.Fatal(err)
			}

			previous := aiProviders[provider]
			defer func() { aiProviders[provider] = previous }()
			var calls [][]aiTurn
			first := true
			aiProviders[provider] = func(_ context.Context, _ config.Config, turns []aiTurn, _ []config.Tool, _ aiRequester) (aiAnswer, error) {
				calls = append(calls, append([]aiTurn(nil), turns...))
				if first {
					first = false
					if tc.cancel {
						if _, err := c.DB.Exec(`UPDATE ai_tasks SET status='cancelled' WHERE id='failed-task'`); err != nil {
							t.Errorf("cancel task: %v", err)
						}
						aiCancelActive(c, "failed-task")
					}
					return aiAnswer{}, errors.New("provider fixture failure")
				}
				return aiAnswer{Text: "answer"}, nil
			}

			aiNamedProcess(c)
			var status string
			if err := c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id='failed-task'`).Scan(&status); err != nil {
				t.Fatal(err)
			}
			wantStatus := "uncertain"
			if tc.cancel {
				wantStatus = "cancelled"
			}
			if status != wantStatus {
				t.Fatalf("first task status=%q want %q", status, wantStatus)
			}
			var contextBlob []byte
			if err := c.DB.QueryRow(`SELECT context FROM ai_sessions WHERE chat_id='failed-context-chat' AND agent_id='ai-one'`).Scan(&contextBlob); err != nil {
				t.Fatal(err)
			}
			contextRaw, err := c.Engine.Open(contextBlob, []byte("ai/context/failed-context-chat/ai-one/1"))
			if err != nil {
				t.Fatal(err)
			}
			var turns []aiTurn
			if err := json.Unmarshal(contextRaw, &turns); err != nil || len(turns) != 0 {
				t.Fatalf("failed task persisted context: turns=%d err=%v", len(turns), err)
			}

			addNamedMessageFixture(t, c, "failed-context-chat", "next-message")
			if _, err := c.DB.Exec(`INSERT INTO ai_tasks(id,chat_id,agent_id,message_id,status,result_id,created_at,updated_at) VALUES('next-task','failed-context-chat','ai-one','next-message','queued','',0,0)`); err != nil {
				t.Fatal(err)
			}
			aiNamedProcess(c)
			if err := c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id='next-task'`).Scan(&status); err != nil || status != "succeeded" {
				t.Fatalf("next task status=%q err=%v", status, err)
			}
			if len(calls) != 2 || len(calls[1]) != 1 || calls[1][0].Role != "user" || calls[1][0].Content != "question" {
				t.Fatalf("next provider turns=%+v; stale failed prompt was persisted", calls)
			}
		})
	}
}

func jsonBody(t *testing.T, value any) *strings.Reader {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(data))
}
