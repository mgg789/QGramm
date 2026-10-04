//go:build qg_openai || qg_anthropic

package modules

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func aiFixture(t *testing.T) (*core.Core, context.CancelFunc) {
	t.Helper()
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`CREATE TABLE users(id TEXT PRIMARY KEY,disabled INTEGER DEFAULT 0);CREATE TABLE devices(id TEXT PRIMARY KEY,user_id TEXT,public_key TEXT,signing_key TEXT,revoked INTEGER DEFAULT 0);
CREATE TABLE chats(id TEXT PRIMARY KEY,kind TEXT,mode TEXT,seq INTEGER DEFAULT 0,epoch INTEGER DEFAULT 0,pending INTEGER DEFAULT 0,created_at INTEGER);
CREATE TABLE members(chat_id TEXT,user_id TEXT,role TEXT,can_send INTEGER,joined_seq INTEGER,active INTEGER DEFAULT 1,PRIMARY KEY(chat_id,user_id));
CREATE TABLE messages(id TEXT PRIMARY KEY,chat_id TEXT,sender TEXT,device_id TEXT,operation_id TEXT,seq INTEGER,revision INTEGER DEFAULT 1,deleted INTEGER DEFAULT 0,payload BLOB,metadata BLOB,epoch INTEGER,created_at INTEGER);
CREATE TABLE events(chat_id TEXT,seq INTEGER,kind TEXT,message_id TEXT,data BLOB,created_at INTEGER,PRIMARY KEY(chat_id,seq));
CREATE TABLE operations(device_id TEXT,operation_id TEXT,hash TEXT,result BLOB,created_at INTEGER,PRIMARY KEY(device_id,operation_id));
INSERT INTO users(id) VALUES('human');INSERT INTO devices(id,user_id,public_key,signing_key) VALUES('phone','human','','');`)
	if e != nil {
		t.Fatal(e)
	}
	engine, e := cryptoenc.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.Defaults()
	cfg.Features.OpenAI = aiProviders["openai"] != nil
	cfg.Features.Anthropic = aiProviders["anthropic"] != nil
	cfg.AI.Model = "mock"
	cfg.AI.OpenAIKeyEnv = "AI_TEST_OPENAI"
	cfg.AI.AnthropicKeyEnv = "AI_TEST_ANTHROPIC"
	t.Setenv(cfg.AI.OpenAIKeyEnv, "nonsecret-test-key")
	t.Setenv(cfg.AI.AnthropicKeyEnv, "nonsecret-test-key")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &core.Core{DB: db, Config: cfg, Engine: engine, Mux: http.NewServeMux(), Context: ctx}
	if e = installAI(c); e != nil {
		t.Fatal(e)
	}
	return c, cancel
}

func TestAIExplicitIdentityAtomicQueueAndLocalDedup(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel() // Suppress external worker calls in this local persistence test.
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"user_id":"human","device_id":"phone","provider":"`+provider+`","mode":"basic","tools":[]}`))
	aiCreateParticipant(c, w, r)
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	var out struct {
		Chat   string `json:"chat_id"`
		User   string `json:"user_id"`
		Device string `json:"device_id"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	public, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	id := core.Identity{UserID: "human", DeviceID: "phone"}
	env, e := cryptoenc.SealEnvelope(public, []byte("hello"), cryptoenc.Binding(out.Chat, id.UserID, id.DeviceID, "op1"))
	if e != nil {
		t.Fatal(e)
	}
	// Sending requires a valid recipient X25519 key for ViewMessage wrapping.
	_, _ = c.DB.Exec(`UPDATE devices SET public_key=? WHERE id='phone'`, c.Engine.PublicKey())
	m, e := c.Send(context.Background(), id, out.Chat, core.MessageInput{OperationID: "op1", Envelope: &env})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Send(context.Background(), id, out.Chat, core.MessageInput{OperationID: "op1", Envelope: &env}); e != nil {
		t.Fatal(e)
	}
	var count int
	var job string
	if c.DB.QueryRow(`SELECT count(*),id FROM ai_jobs`).Scan(&count, &job) != nil || count != 1 {
		t.Fatal("non-atomic or duplicate AI queue")
	}
	_, _ = c.DB.Exec(`UPDATE ai_jobs SET status='running' WHERE id=?`, job)
	aiComplete(context.Background(), c, job, out.Chat, out.User, out.Device, "basic", 0, nil, []aiTurn{{Role: "user", Content: "hello"}}, "answer")
	aiComplete(context.Background(), c, job, out.Chat, out.User, out.Device, "basic", 0, nil, []aiTurn{{Role: "user", Content: "hello"}}, "answer")
	if e = c.DB.QueryRow(`SELECT count(*) FROM messages WHERE sender=?`, out.User).Scan(&count); e != nil || count != 1 {
		t.Fatal("AI local result dedup failed")
	}
	var payload []byte
	var resultID, status string
	if c.DB.QueryRow(`SELECT status,result_id FROM ai_jobs WHERE id=?`, job).Scan(&status, &resultID) != nil || status != "succeeded" {
		t.Fatal("job not completed")
	}
	if c.DB.QueryRow(`SELECT payload FROM messages WHERE id=?`, resultID).Scan(&payload) != nil || bytes.Contains(payload, []byte("answer")) {
		t.Fatal("plaintext persisted")
	}
	plain, e := c.Engine.Open(payload, []byte("message/"+resultID))
	if e != nil || string(plain) != "answer" {
		t.Fatal("output payload invalid")
	}
	_ = m
}

func TestAINetworkPrivateAndRedirectBoundaries(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer target.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	if _, e := aiRequest(ctx, target.URL, map[string]string{}, map[string]any{}, false); e == nil || reached {
		t.Fatal("private provider endpoint accepted")
	}
	if _, e := aiRequest(ctx, target.URL, map[string]string{}, map[string]any{}, true); e != nil || !reached {
		t.Fatal("explicit private tool permission failed")
	}
	reached = false
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	if _, e := aiRequest(ctx, redirect.URL, map[string]string{"Authorization": "Bearer private-test"}, map[string]any{}, true); e == nil || reached {
		t.Fatal("redirect leaked tool credential")
	}
}

func TestAIToolSchemaFailsClosed(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false}
	if aiCheckArguments(schema, json.RawMessage(`{"query":"ok"}`)) != nil {
		t.Fatal("valid schema rejected")
	}
	for _, raw := range []string{`{"query":1}`, `{"query":"ok","url":"http://internal"}`, `{}`} {
		if aiCheckArguments(schema, json.RawMessage(raw)) == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	schema["oneOf"] = []any{}
	if aiCheckArguments(schema, json.RawMessage(`{"query":"ok"}`)) == nil {
		t.Fatal("unsupported schema ignored")
	}
}

func TestAIRestartMarksInflightUncertainWithoutRetry(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	_, e := c.DB.Exec(`INSERT INTO ai_jobs VALUES('crashed','chat','message','running','',0,0)`)
	if e != nil {
		t.Fatal(e)
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	restarted := &core.Core{DB: c.DB, Config: c.Config, Engine: c.Engine, Mux: http.NewServeMux(), Context: ctx}
	if e = installAI(restarted); e != nil {
		t.Fatal(e)
	}
	var status string
	if c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id='crashed'`).Scan(&status) != nil || status != "uncertain" {
		t.Fatal("unsafe in-flight retry")
	}
}

func TestAIWorkerClaimsSerializeChatAndParallelizeIndependentChats(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	_, _ = c.DB.Exec(`UPDATE devices SET public_key=? WHERE id='phone'`, c.Engine.PublicKey())
	chats := []string{}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]any{"user_id": "human", "device_id": "phone", "provider": provider, "mode": "basic", "tools": []string{}})
		aiCreateParticipant(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
		if w.Code != 201 {
			t.Fatalf("participant %d %s", w.Code, w.Body)
		}
		var out struct {
			Chat string `json:"chat_id"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		chats = append(chats, out.Chat)
	}
	public, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	id := core.Identity{UserID: "human", DeviceID: "phone"}
	for i, chat := range []string{chats[0], chats[0], chats[1]} {
		operation := []string{"parallel-op1", "parallel-op2", "parallel-op3"}[i]
		envelope, e := cryptoenc.SealEnvelope(public, []byte("question"), cryptoenc.Binding(chat, id.UserID, id.DeviceID, operation))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.Send(context.Background(), id, chat, core.MessageInput{OperationID: operation, Envelope: &envelope}); e != nil {
			t.Fatal(e)
		}
	}
	// The fixture's background pool is canceled. These runners exercise exactly
	// the same claim/execution path with a blocking local provider replacement.
	manual := &core.Core{DB: c.DB, Config: c.Config, Engine: c.Engine, Context: context.Background()}
	started := make(chan string, 3)
	release := make(chan struct{})
	var runners sync.WaitGroup
	var unblock sync.Once
	defer func() { unblock.Do(func() { close(release) }); runners.Wait() }()
	runner := func(ctx context.Context, c *core.Core, job, provider string, turns []aiTurn, tools []config.Tool) (string, error) {
		var chat string
		if e := c.DB.QueryRowContext(ctx, `SELECT chat_id FROM ai_jobs WHERE id=?`, job).Scan(&chat); e != nil {
			return "", e
		}
		started <- chat
		select {
		case <-release:
			return "answer", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	for i := 0; i < 3; i++ {
		runners.Add(1)
		go func() { defer runners.Done(); aiNextWith(manual, runner) }()
	}
	var first, second string
	select {
	case first = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first chat not claimed")
	}
	select {
	case second = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("independent chat did not run concurrently")
	}
	if first == second {
		t.Fatal("same chat claimed twice")
	}
	var running, queued int
	if e := c.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE status='running'`).Scan(&running); e != nil || running != 2 {
		t.Fatal("independent claims not simultaneously running")
	}
	if e := c.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE chat_id=? AND status='queued'`, chats[0]).Scan(&queued); e != nil || queued != 1 {
		t.Fatal("same-chat serialization lost")
	}
	unblock.Do(func() { close(release) })
	runners.Wait()
	var succeeded int
	if e := c.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE status='succeeded'`).Scan(&succeeded); e != nil || succeeded != 2 {
		t.Fatal("parallel results not durably completed")
	}
}
