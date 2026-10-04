//go:build qg_openai

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

// Explicit opt-in only; normal test runs never spend provider credits.
func TestLiveOpenAICompatibleEncryptedConversation(t *testing.T) {
	if os.Getenv("QGRAMM_LIVE_TEST") != "1" {
		t.Skip("live provider credentials and explicit opt-in required")
	}
	cfg := config.Defaults()
	cfg.Features.OpenAI = true
	cfg.Server.AllowInsecureLoopback = true
	cfg.Capacity = config.DeriveCapacity(cfg.Capacity, config.DetectResources())
	cfg.Storage.Path = filepath.Join(t.TempDir(), "live.db")
	cfg.Storage.Files = t.TempDir()
	cfg.AI.OpenAIURL = os.Getenv("QGRAMM_LIVE_URL")
	cfg.AI.Model = os.Getenv("QGRAMM_LIVE_MODEL")
	cfg.AI.OpenAIKeyEnv = "QGRAMM_LIVE_KEY"
	if os.Getenv(cfg.AI.OpenAIKeyEnv) == "" {
		t.Fatal("live credential unavailable")
	}
	public, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(cfg.Security.TokenPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.Security.ManagementSecretEnv, strings.Repeat("test-management-", 3))
	for _, name := range []string{cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		key := make([]byte, 32)
		rand.Read(key)
		t.Setenv(name, base64.StdEncoding.EncodeToString(key))
	}
	c, err := core.Open(cfg, []string{"openai"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	call := func(method, path string, body any, status int) []byte {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+os.Getenv(cfg.Security.ManagementSecretEnv))
		w := httptest.NewRecorder()
		c.Handler().ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("management status=%d expected=%d", w.Code, status)
		}
		return w.Body.Bytes()
	}
	call("PUT", "/management/v1/users/human", map[string]bool{"disabled": false}, 200)
	recipientKey := make([]byte, 32)
	rand.Read(recipientKey)
	recipient, _ := cryptoenc.New(bytes.Repeat([]byte{1}, 32), recipientKey)
	call("PUT", "/management/v1/users/human/devices/phone", map[string]string{"public_key": recipient.PublicKey()}, 200)
	raw := call("POST", "/management/v1/ai/participants", map[string]any{"user_id": "human", "device_id": "phone", "provider": "openai", "mode": "basic", "tools": []string{}}, 201)
	var chat struct {
		ID string `json:"chat_id"`
	}
	if json.Unmarshal(raw, &chat) != nil || chat.ID == "" {
		t.Fatal("invalid chat response")
	}
	serverPublic, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	envelope, err := cryptoenc.SealEnvelope(serverPublic, []byte("Reply with the short literal QGRAMM_ACCEPTED. This is a synthetic integration test."), cryptoenc.Binding(chat.ID, "human", "phone", "live-one"))
	if err != nil {
		t.Fatal(err)
	}
	id := core.Identity{UserID: "human", DeviceID: "phone"}
	input := core.MessageInput{OperationID: "live-one", Envelope: &envelope}
	if _, err = c.Send(context.Background(), id, chat.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Send(context.Background(), id, chat.ID, input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var status, result string
		err = c.DB.QueryRow(`SELECT status,result_id FROM ai_jobs WHERE chat_id=?`, chat.ID).Scan(&status, &result)
		if err == nil && status == "succeeded" {
			message, e := c.ViewMessage(context.Background(), id, result)
			if e != nil || message.Envelope == nil {
				t.Fatal("reply not available")
			}
			plain, e := recipient.OpenEnvelope(*message.Envelope, cryptoenc.Binding(chat.ID, "human", "phone", result))
			if e != nil || len(plain) == 0 {
				t.Fatal("reply cannot be decrypted")
			}
			var n int
			if c.DB.QueryRow(`SELECT count(*) FROM ai_jobs WHERE chat_id=?`, chat.ID).Scan(&n) != nil || n != 1 {
				t.Fatal("duplicate paid job")
			}
			t.Log("live encrypted input, durable dedup, provider reply and recipient decryption passed; reply content omitted")
			return
		}
		if status == "failed" || status == "uncertain" {
			t.Fatalf("live job ended %s; provider payload omitted", status)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("live job timed out")
}
