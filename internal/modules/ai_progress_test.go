//go:build qg_ai_streaming && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func progressFixture(t *testing.T) (*core.Core, string, string, string) {
	t.Helper()
	c, cancel := aiFixture(t)
	c.Config.Features.AIStreaming = true
	if err := installAIProgress(c); err != nil {
		t.Fatal(err)
	}
	cancel()
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	w := httptest.NewRecorder()
	aiCreateParticipant(c, w, httptest.NewRequest("POST", "/", strings.NewReader(`{"user_id":"human","device_id":"phone","provider":"`+provider+`"}`)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var p struct {
		Chat   string `json:"chat_id"`
		User   string `json:"user_id"`
		Device string `json:"device_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	payload, _ := c.Engine.Seal([]byte("question"), []byte("message/source"))
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`UPDATE devices SET public_key=? WHERE id='phone'`, []any{c.Engine.PublicKey()}},
		{`UPDATE chats SET seq=1 WHERE id=?`, []any{p.Chat}},
		{`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES('source',?,'human','phone','ask',1,?,'{}',0,0)`, []any{p.Chat, payload}},
		{`INSERT INTO ai_jobs VALUES('progress-job',?,'source','running','',0,0)`, []any{p.Chat}},
	} {
		if _, err := c.DB.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return c, p.Chat, p.User, p.Device
}

func TestProgressEncryptedReplayRevocationAndExpiry(t *testing.T) {
	c, chat, user, device := progressFixture(t)
	ctx := context.Background()
	if err := writeAIProgress(ctx, c, "progress-job", "text.delta", json.RawMessage(`{"text":"private output"}`)); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := c.DB.QueryRow(`SELECT payload FROM ai_progress`).Scan(&stored); err != nil || bytes.Contains(stored, []byte("private output")) {
		t.Fatal("plaintext progress", err)
	}
	read := func(after string, id core.Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/?after="+after, nil)
		r.SetPathValue("chat", chat)
		r.SetPathValue("job", "progress-job")
		w := httptest.NewRecorder()
		readAIProgress(c, w, r, id)
		return w
	}
	id := core.Identity{UserID: "human", DeviceID: "phone"}
	w := read("0", id)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Chunks []struct {
			Envelope  cryptoenc.Envelope `json:"envelope"`
			Operation string             `json:"operation_id"`
		} `json:"chunks"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Chunks) != 1 {
		t.Fatal(w.Body.String())
	}
	plain, err := c.Engine.OpenEnvelope(response.Chunks[0].Envelope, cryptoenc.Binding(chat, user, device, response.Chunks[0].Operation))
	if err != nil || string(plain) != `{"text":"private output"}` {
		t.Fatal("invalid wrapped progress", err)
	}
	if w = read("1", id); w.Code != 200 || strings.Contains(w.Body.String(), `"chunk":1`) {
		t.Fatal("cursor replay", w.Body.String())
	}
	if w = read("0", core.Identity{UserID: "outsider", DeviceID: "phone"}); w.Code != 403 {
		t.Fatal("crossuser progress", w.Code)
	}
	_, _ = c.DB.Exec(`DELETE FROM ai_progress`)
	if w = read("0", id); w.Code != 410 {
		t.Fatal("purged progress silently lost", w.Code)
	}
	_, _ = c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='phone'`)
	if err = writeAIProgress(ctx, c, "progress-job", "text.delta", json.RawMessage(`{"text":"late"}`)); err == nil {
		t.Fatal("revoked source published output")
	}
	if w = read("1", id); w.Code != 403 {
		t.Fatal("revoked read", w.Code)
	}
}

func TestProgressCancelRetainsStatusAndRejectsPendingFlush(t *testing.T) {
	c, chat, _, _ := progressFixture(t)
	ctx, flush := prepareAIProgress(context.Background(), c, "progress-job")
	emit := aiStreamCallbackFromContext(ctx)
	if emit == nil {
		t.Fatal("stream callback missing")
	}
	if err := emit("text.delta", json.RawMessage(`{"text":"first"}`)); err != nil {
		t.Fatal(err)
	}
	if err := emit("text.delta", json.RawMessage(`{"text":"pending"}`)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", nil)
	r.SetPathValue("chat", chat)
	r.SetPathValue("job", "progress-job")
	w := httptest.NewRecorder()
	cancelAIJob(c, w, r, core.Identity{UserID: "human", DeviceID: "phone"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	aiFinishError(c, "progress-job", "uncertain")
	var status string
	_ = c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id='progress-job'`).Scan(&status)
	if status != "cancelled" {
		t.Fatal("cancel overwritten", status)
	}
	if err := flush(); err == nil {
		t.Fatal("cancelled pending flush succeeded")
	}
	if err := writeAIProgress(context.Background(), c, "progress-job", "text.delta", json.RawMessage(`{"text":"late"}`)); err == nil {
		t.Fatal("cancelled output persisted")
	}
}

func TestProgressCancellationRacesFinalCommit(t *testing.T) {
	for attempt := 0; attempt < 12; attempt++ {
		c, chat, user, device := progressFixture(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			aiComplete(context.Background(), c, "progress-job", chat, user, device, "basic", 0, nil, nil, "final")
		}()
		w := httptest.NewRecorder()
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("POST", "/", nil)
			r.SetPathValue("chat", chat)
			r.SetPathValue("job", "progress-job")
			cancelAIJob(c, w, r, core.Identity{UserID: "human", DeviceID: "phone"})
		}()
		close(start)
		wg.Wait()
		var status string
		var outputs int
		if err := c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id='progress-job'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if err := c.DB.QueryRow(`SELECT count(*) FROM messages WHERE sender=?`, user).Scan(&outputs); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || (status == "cancelled" && outputs != 0) || (status == "succeeded" && outputs != 1) || (status != "cancelled" && status != "succeeded") {
			t.Fatalf("cancel/final outcome: status=%s outputs=%d cancelHTTP=%d", status, outputs, w.Code)
		}
	}
}

func TestLegacyFailedPromptDoesNotLeakIntoNextContext(t *testing.T) {
	c, chat, _, _ := progressFixture(t)
	manual := &core.Core{DB: c.DB, Config: c.Config, Engine: c.Engine, Context: context.Background()}
	if _, err := c.DB.Exec(`UPDATE ai_jobs SET status='queued' WHERE id='progress-job'`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	aiNextWith(manual, func(_ context.Context, _ *core.Core, _, _ string, turns []aiTurn, _ []config.Tool) (string, error) {
		calls++
		if len(turns) != 1 || turns[0].Content != "question" {
			t.Fatal("incorrect initial context")
		}
		return "", errors.New("unknown provider outcome")
	})
	var encrypted []byte
	if err := c.DB.QueryRow(`SELECT context FROM ai_chats WHERE chat_id=?`, chat).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	plain, err := c.Engine.Open(encrypted, []byte("ai/context/"+chat))
	if err != nil || string(plain) != "[]" || calls != 1 {
		t.Fatalf("failed prompt persisted: %q calls=%d err=%v", plain, calls, err)
	}
	payload, err := c.Engine.Seal([]byte("next question"), []byte("message/second-source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,payload,metadata,epoch,created_at) VALUES('second-source',?,'human','phone','second-ask',2,?,'{}',0,1)`, chat, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`INSERT INTO ai_jobs VALUES('second-job',?,'second-source','queued','',1,1)`, chat); err != nil {
		t.Fatal(err)
	}
	aiNextWith(manual, func(_ context.Context, _ *core.Core, _, _ string, turns []aiTurn, _ []config.Tool) (string, error) {
		calls++
		if len(turns) != 1 || turns[0].Content != "next question" {
			t.Fatalf("failed input reached next provider: %+v", turns)
		}
		return "answer", nil
	})
	if calls != 2 {
		t.Fatal("second job did not execute")
	}
}
