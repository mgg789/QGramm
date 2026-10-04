//go:build qg_e2ee && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestAIAdmittedMLSParticipantDurableBidirectionalBinding(t *testing.T) {
	c, cancel := aiFixture(t)
	c.Config.Features.E2EE = true
	if _, e := c.DB.Exec(mlsSchema); e != nil {
		t.Fatal(e)
	}
	human, e := cryptoenc.NewMLS([]byte("phone"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.DB.Exec(`UPDATE devices SET signing_key=?,public_key=? WHERE id='phone'`, base64.StdEncoding.EncodeToString(human.SigningPublicKey()), c.Engine.PublicKey())
	if e != nil {
		t.Fatal(e)
	}
	provider := "openai"
	if aiProviders[provider] == nil {
		provider = "anthropic"
	}
	body, _ := json.Marshal(map[string]any{"user_id": "human", "device_id": "phone", "provider": provider, "mode": "e2ee", "key_package": base64.StdEncoding.EncodeToString(human.KeyPackage()), "tools": []string{}})
	w := httptest.NewRecorder()
	aiCreateParticipant(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	cancel()
	var out struct {
		Chat    string `json:"chat_id"`
		User    string `json:"user_id"`
		Device  string `json:"device_id"`
		Welcome string `json:"welcome"`
		Epoch   int64  `json:"epoch"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	welcome, _ := base64.StdEncoding.DecodeString(out.Welcome)
	if _, e = human.Join(welcome); e != nil {
		t.Fatal(e)
	}
	if out.Epoch != int64(human.Epoch()) {
		t.Fatal("epoch mismatch")
	}
	var state []byte
	if c.DB.QueryRow(`SELECT state FROM ai_chats WHERE chat_id=?`, out.Chat).Scan(&state) != nil {
		t.Fatal("state absent")
	}
	binding := cryptoenc.Binding(out.Chat, "human", "phone", "human-op")
	wire, e := human.Encrypt([]byte("private question"), binding)
	if e != nil {
		t.Fatal(e)
	}
	plain, consumed, e := aiOpenMLS(c, out.Chat, state, wire, binding)
	if e != nil || string(plain) != "private question" {
		t.Fatalf("AI decrypt %q %v", plain, e)
	}
	answerBinding := cryptoenc.Binding(out.Chat, out.User, out.Device, "ai-op")
	answer, persisted, e := aiSealMLS(c, out.Chat, consumed, []byte("private answer"), answerBinding)
	if e != nil {
		t.Fatal(e)
	}
	plain, e = human.Decrypt(answer, answerBinding)
	if e != nil || string(plain) != "private answer" {
		t.Fatalf("human decrypt %q %v", plain, e)
	}
	if bytes.Contains(persisted, []byte("private answer")) {
		t.Fatal("plaintext snapshot")
	}
	if _, _, e = aiOpenMLS(c, out.Chat, persisted, wire, binding); e == nil {
		t.Fatal("replayed human MLS message accepted")
	}
	// A mismatched device cannot admit an unrelated identity or signing key.
	bad := strings.Replace(string(body), `"device_id":"phone"`, `"device_id":"other"`, 1)
	badResponse := httptest.NewRecorder()
	aiCreateParticipant(c, badResponse, httptest.NewRequest("POST", "/", strings.NewReader(bad)))
	if badResponse.Code != 400 {
		t.Fatal("unregistered device admitted")
	}
}
