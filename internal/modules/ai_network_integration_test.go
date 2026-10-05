//go:build qg_openai && qg_ai_streaming

package modules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestNamedNetworkTwoBotsLocalNoAuthStreamingAndExactRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("no-auth endpoint received credentials")
		}
		var body struct {
			Stream    bool `json:"stream"`
			MaxTokens int  `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Stream || body.MaxTokens != 7 || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[0].Content != "configured instruction" || body.Messages[1].Content != "private question" {
			t.Error("effective profile not applied", err)
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"private answer\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	c, stop := aiFixture(t)
	c.Config.Features.AIStreaming = true
	if err := installAIProgress(c); err != nil {
		t.Fatal(err)
	}
	stop()
	// Wait for the old fixture pool to exit before changing its context. No
	// production worker or crypto state is run concurrently with this test.
	deadline := time.Now().Add(time.Second)
	for {
		_, present := aiInstalled.Load(c)
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture workers did not stop")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Context = ctx
	yes := true
	tokens := 7
	c.Config.Features.Groups = true
	c.Config.AI.Endpoints = map[string]config.AIEndpoint{"local": {Provider: "openai", URL: server.URL, Model: "local-test", Auth: "none", AllowPrivate: &yes, Streaming: &yes, MaxOutputTokens: &tokens, Capabilities: []string{"basic_text"}}}
	c.Config.AI.Bots = map[string]config.AIBot{"first": {Endpoint: "local", SystemPrompt: "configured instruction", Tools: []string{}}, "second": {Endpoint: "local", SystemPrompt: "configured instruction", Tools: []string{}}}
	c.Config.AI.DefaultBot = "first"
	for _, bot := range []string{"first", "second"} {
		w := httptest.NewRecorder()
		aiCreateNamedAgent(c, w, httptest.NewRequest("POST", "/", strings.NewReader(fmt.Sprintf(`{"bot_name":%q,"user_id":%q}`, bot, "ai-"+bot))))
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := c.DB.Exec(`UPDATE devices SET public_key=? WHERE id='phone'`, c.Engine.PublicKey()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c.CreateChat(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"id":"network-chat","mode":"basic","members":["human","ai-first","ai-second"]}`)), "group")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, agent := range []string{"ai-first", "ai-second"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"tools":[]}`))
		r.SetPathValue("chat", "network-chat")
		r.SetPathValue("agent", agent)
		w = httptest.NewRecorder()
		aiAttachNamedAgent(c, w, r)
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	public, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	env, err := cryptoenc.SealEnvelope(public, []byte("private question"), cryptoenc.Binding("network-chat", "human", "phone", "network-op"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Send(ctx, core.Identity{UserID: "human", DeviceID: "phone"}, "network-chat", core.MessageInput{OperationID: "network-op", Envelope: &env})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"message_id": m.ID, "agents": []string{"ai-first", "ai-second"}})
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
		r.SetPathValue("chat", "network-chat")
		w = httptest.NewRecorder()
		aiQueueNamedTasks(c, w, r, core.Identity{UserID: "human", DeviceID: "phone"})
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	aiNamedProcess(c)
	aiNamedProcess(c)
	aiNamedProcess(c)
	var succeeded, chunks, outputs int
	if err = c.DB.QueryRow(`SELECT count(*) FROM ai_tasks WHERE status='succeeded'`).Scan(&succeeded); err != nil || succeeded != 2 {
		t.Fatal("tasks did not complete", succeeded, err)
	}
	_ = c.DB.QueryRow(`SELECT count(*) FROM ai_progress`).Scan(&chunks)
	_ = c.DB.QueryRow(`SELECT count(*) FROM messages WHERE sender IN('ai-first','ai-second')`).Scan(&outputs)
	if calls.Load() != 2 || chunks != 2 || outputs != 2 {
		t.Fatalf("duplicate/missing work calls=%d chunks=%d outputs=%d", calls.Load(), chunks, outputs)
	}
	rows, err := c.DB.Query(`SELECT payload FROM messages WHERE sender IN('ai-first','ai-second')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var stored []byte
		if err = rows.Scan(&stored); err != nil || bytes.Contains(stored, []byte("private answer")) {
			t.Fatal("plaintext output persisted", err)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
}
