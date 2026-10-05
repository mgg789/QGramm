//go:build qg_ai_streaming && qg_e2ee && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestProgressMLSUsesDurableRatchetForChunksAndFinalMessage(t *testing.T) {
	c, chat, user, device := progressFixture(t)
	ai, err := cryptoenc.NewMLS([]byte(device))
	if err != nil {
		t.Fatal(err)
	}
	human, err := cryptoenc.NewMLS([]byte("phone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ai.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	_, welcome, err := ai.Invite(human.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = human.Join(welcome); err != nil {
		t.Fatal(err)
	}
	state, err := ai.Snapshot(c.Engine, []byte("ai/state/"+chat))
	if err != nil {
		t.Fatal(err)
	}
	epoch := int64(ai.Epoch())
	if _, err = c.DB.Exec(`UPDATE chats SET mode='e2ee',epoch=? WHERE id=?`, epoch, chat); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE ai_chats SET state=? WHERE chat_id=?`, state, chat); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		payload := json.RawMessage(fmt.Sprintf(`{"text":"part%d"}`, i))
		if err = writeAIProgress(context.Background(), c, "progress-job", "text.delta", payload); err != nil {
			t.Fatal(err)
		}
		var stored []byte
		if err = c.DB.QueryRow(`SELECT payload FROM ai_progress WHERE job_id='progress-job' AND chunk=?`, i).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		wire, err := c.Engine.Open(stored, []byte(fmt.Sprintf("ai/progress/progress-job/%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		plain, err := human.Decrypt(wire, cryptoenc.Binding(chat, user, device, fmt.Sprintf("ai-progress-progress-job-%d", i)))
		if err != nil || string(plain) != string(payload) {
			t.Fatalf("chunk%d %s %v", i, plain, err)
		}
	}
	// Pass the intentionally stale pre-stream state: completion must reload the
	// durable snapshot rather than replaying an already-used sender generation.
	aiComplete(context.Background(), c, "progress-job", chat, user, device, "e2ee", epoch, state, []aiTurn{{Role: "user", Content: "question"}}, "final answer")
	var message string
	var stored []byte
	if err = c.DB.QueryRow(`SELECT id,payload FROM messages WHERE operation_id='ai-progress-job'`).Scan(&message, &stored); err != nil {
		t.Fatal(err)
	}
	wire, err := c.Engine.Open(stored, []byte("message/"+message))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := human.Decrypt(wire, cryptoenc.Binding(chat, user, device, "ai-progress-job"))
	if err != nil || string(plain) != "final answer" {
		t.Fatalf("final ratchet reused: %q %v", plain, err)
	}
}
