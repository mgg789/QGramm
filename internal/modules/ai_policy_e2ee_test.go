//go:build qg_ai_policy && qg_openai && qg_e2ee

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestAIPolicyMLSProviderApprovalResumeDoesNotReplayRatchet(t *testing.T) {
	base, cancel := aiFixture(t)
	cancel()
	c := &core.Core{DB: base.DB, Engine: base.Engine, Config: base.Config, Context: context.Background(), Mux: http.NewServeMux(), InTransaction: base.InTransaction}
	c.Config.Features.E2EE = true
	c.Config.Features.AIPolicy = true
	c.Config.AIPolicy.RequireProviderApproval = true
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(c.Config.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	if err := installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	if err := installE2EE(c); err != nil {
		t.Fatal(err)
	}
	human, err := cryptoenc.NewMLS([]byte("phone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE devices SET signing_key=?,public_key=? WHERE id='phone'`, base64.StdEncoding.EncodeToString(human.SigningPublicKey()), c.Engine.PublicKey()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"private answer"}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
	}))
	defer provider.Close()
	c.Config.AI.OpenAIURL = provider.URL
	c.Config.AI.AllowPrivate = true
	body, _ := json.Marshal(map[string]any{"user_id": "human", "device_id": "phone", "provider": "openai", "mode": "e2ee", "key_package": base64.StdEncoding.EncodeToString(human.KeyPackage()), "tools": []string{}})
	w := httptest.NewRecorder()
	aiCreateParticipant(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var participant struct {
		Chat    string `json:"chat_id"`
		Welcome string `json:"welcome"`
		Epoch   int64  `json:"epoch"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &participant); err != nil {
		t.Fatal(err)
	}
	welcome, _ := base64.StdEncoding.DecodeString(participant.Welcome)
	if _, err = human.Join(welcome); err != nil {
		t.Fatal(err)
	}
	wire, err := human.Encrypt([]byte("private question"), cryptoenc.Binding(participant.Chat, "human", "phone", "question"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Send(context.Background(), core.Identity{UserID: "human", DeviceID: "phone"}, participant.Chat, core.MessageInput{OperationID: "question", MLS: base64.StdEncoding.EncodeToString(wire), Epoch: participant.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	aiNext(c)
	var job, request, status string
	if err = c.DB.QueryRow(`SELECT id,status FROM ai_jobs`).Scan(&job, &status); err != nil || status != "awaiting_approval" || calls.Load() != 0 {
		t.Fatalf("not gated: %s %v calls=%d", status, err, calls.Load())
	}
	if err = c.DB.QueryRow(`SELECT id FROM ai_policy_requests WHERE status='awaiting'`).Scan(&request); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("request", request)
	w = httptest.NewRecorder()
	aiPolicyRequest(c, w, r)
	var metadata map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &metadata) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	claims := jwt.MapClaims{"v": 1, "jti": "mls-approval", "iss": c.Config.AIPolicy.Issuer, "aud": c.Config.AIPolicy.Audience, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	for _, field := range []string{"request_id", "job_id", "chat_id", "source_user", "source_device", "ai_user", "ai_device", "epoch", "action", "name", "request_hash", "destination_hash"} {
		claims[field] = metadata[field]
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(map[string]string{"token": token})
	w = httptest.NewRecorder()
	aiPolicyApprove(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	aiNext(c)
	if err = c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id=?`, job).Scan(&status); err != nil || status != "succeeded" || calls.Load() != 1 {
		t.Fatalf("resume: %s %v calls=%d", status, err, calls.Load())
	}
	var messageID, chat, sender, device, operation string
	var payload []byte
	if err = c.DB.QueryRow(`SELECT id,chat_id,sender,device_id,operation_id,payload FROM messages WHERE sender!='human'`).Scan(&messageID, &chat, &sender, &device, &operation, &payload); err != nil {
		t.Fatal(err)
	}
	payload, err = c.Engine.Open(payload, []byte("message/"+messageID))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := human.Decrypt(payload, cryptoenc.Binding(chat, sender, device, operation))
	if err != nil || string(plain) != "private answer" {
		t.Fatalf("MLS answer: %q %v", plain, err)
	}
	aiNext(c)
	if calls.Load() != 1 {
		t.Fatal("provider replay")
	}
}
