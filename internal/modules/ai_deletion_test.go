//go:build qg_openai || qg_anthropic

package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAIGlobalDeleteClearsCapturedContextAndBlocksLateReply(t *testing.T) {
	c, cancel := aiFixture(t)
	cancel()
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	w := httptest.NewRecorder()
	aiCreateParticipant(c, w, httptest.NewRequest("POST", "/", strings.NewReader(`{"user_id":"human","device_id":"phone","provider":"`+provider+`","mode":"basic","tools":[]}`)))
	if w.Code != 201 {
		t.Fatal("create", w.Code)
	}
	var out struct {
		Chat   string `json:"chat_id"`
		User   string `json:"user_id"`
		Device string `json:"device_id"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	turns := []aiTurn{{Role: "user", Content: "deleted private context"}}
	raw, _ := json.Marshal(turns)
	blob, _ := c.Engine.Seal(raw, []byte("ai/context/"+out.Chat))
	if _, e := c.DB.Exec(`UPDATE ai_chats SET context=? WHERE chat_id=?`, blob, out.Chat); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DB.Exec(`INSERT INTO messages(id,chat_id,sender,payload,metadata,deleted) VALUES('source',?,'human',X'','{}',1)`, out.Chat); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DB.Exec(`INSERT INTO ai_jobs VALUES('running',?,'source','running','',0,0)`, out.Chat); e != nil {
		t.Fatal(e)
	}
	tx, e := c.DB.BeginTx(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, hook := range c.OnDelete {
		if e = hook(context.Background(), tx, "source"); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var contextBlob []byte
	var status string
	c.DB.QueryRow(`SELECT context FROM ai_chats WHERE chat_id=?`, out.Chat).Scan(&contextBlob)
	plain, e := c.Engine.Open(contextBlob, []byte("ai/context/"+out.Chat))
	if e != nil || !bytes.Equal(plain, []byte("[]")) {
		t.Fatal("deleted context retained")
	}
	c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id='running'`).Scan(&status)
	if status != "failed" {
		t.Fatal("captured job retained", status)
	}
	aiComplete(context.Background(), c, "running", out.Chat, out.User, out.Device, "basic", 0, nil, turns, "late reply")
	var count int
	c.DB.QueryRow(`SELECT count(*) FROM messages WHERE sender=?`, out.User).Scan(&count)
	if count != 0 {
		t.Fatal("late reply resurrected deleted context")
	}
}
