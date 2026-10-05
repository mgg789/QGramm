//go:build qg_ai_policy && qg_openai && qg_http_tools

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestAIPolicyRealHTTPApprovalResumesWithoutProviderReplay(t *testing.T) {
	base, cancel := aiFixture(t)
	cancel()
	c := &core.Core{DB: base.DB, Engine: base.Engine, Config: base.Config, Context: context.Background(), Mux: http.NewServeMux(), InTransaction: base.InTransaction, OnDelete: base.OnDelete}
	c.Config.Features.AIPolicy = true
	c.Config.Features.HTTPTools = true
	c.Config.AIPolicy.ProviderReserveMicrounits = 100
	c.Config.AIPolicy.GlobalDailyBudgetMicrounits = 1000
	c.Config.AI.InputPriceMicrounitsPerMillionTokens = 1000000
	c.Config.AI.OutputPriceMicrounitsPerMillionTokens = 2000000
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(c.Config.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	if err := installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	var providerCalls, toolCalls atomic.Int32
	var noticeBeforeEffect atomic.Bool
	noticeBeforeEffect.Store(true)
	checkNotice := func(want int) {
		var count int
		if c.DB.QueryRow(`SELECT count(*) FROM events WHERE kind='ai.egress.notice'`).Scan(&count) != nil || count != want {
			noticeBeforeEffect.Store(false)
		}
	}
	toolServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolCalls.Add(1)
		checkNotice(2)
		if r.Method != "POST" {
			http.Error(w, "method", 400)
			return
		}
		var args map[string]string
		if json.NewDecoder(r.Body).Decode(&args) != nil || args["query"] != "sensitive input" {
			http.Error(w, "arguments", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"answer":"tool result"}`)
	}))
	defer toolServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			checkNotice(1)
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"lookup-1","function":{"name":"lookup","arguments":"{\"query\":\"sensitive input\"}"}}]}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
			return
		}
		checkNotice(3)
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) < 3 || body.Messages[len(body.Messages)-1].Role != "tool" {
			http.Error(w, "missing resumed tool result", 400)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"final answer"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`)
	}))
	defer providerServer.Close()
	configureNamedBotFixture(c, "openai")
	endpoint := c.Config.AI.Endpoints["named-test"]
	endpoint.URL = providerServer.URL + "/v1"
	c.Config.AI.Endpoints["named-test"] = endpoint
	c.Config.AI.Bots["ai-one"] = config.AIBot{Endpoint: "named-test", Tools: []string{"lookup"}}
	c.Config.AI.Tools = []config.Tool{{Name: "lookup", Kind: "http", URL: toolServer.URL + "/query?private=test-marker", Methods: []string{"POST"}, AllowPrivate: true, RequireApproval: true, CostMicrounits: 10, Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false}}}
	w := httptest.NewRecorder()
	aiCreateNamedAgent(c, w, httptest.NewRequest("POST", "/", strings.NewReader(`{"bot_name":"ai-one","user_id":"ai-one"}`)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	addNamedChatFixture(t, c, "policy-chat", "ai-one")
	if _, err := c.DB.Exec(`UPDATE ai_sessions SET tools='["lookup"]' WHERE chat_id='policy-chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE devices SET public_key=? WHERE id='phone'`, c.Engine.PublicKey()); err != nil {
		t.Fatal(err)
	}
	serverKey, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	envelope, err := cryptoenc.SealEnvelope(serverKey, []byte("private question"), cryptoenc.Binding("policy-chat", "human", "phone", "ask-policy"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := c.Send(context.Background(), core.Identity{UserID: "human", DeviceID: "phone"}, "policy-chat", core.MessageInput{OperationID: "ask-policy", Envelope: &envelope})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"message_id":"`+message.ID+`","agents":["ai-one"]}`))
	r.SetPathValue("chat", "policy-chat")
	w = httptest.NewRecorder()
	aiQueueNamedTasks(c, w, r, core.Identity{UserID: "human", DeviceID: "phone"})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job, requestID, status string
	if err = c.DB.QueryRow(`SELECT id FROM ai_tasks`).Scan(&job); err != nil {
		t.Fatal(err)
	}
	aiNamedProcess(c)
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id=?`, job).Scan(&status); err != nil || status != "awaiting_approval" {
		t.Fatalf("expected pause, got %s: %v", status, err)
	}
	if providerCalls.Load() != 1 || toolCalls.Load() != 0 {
		t.Fatal("unapproved effect executed")
	}
	if err = c.DB.QueryRow(`SELECT id FROM ai_policy_requests WHERE status='awaiting'`).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err = c.DB.QueryRow(`SELECT payload FROM ai_policy_requests WHERE id=?`, requestID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("private question")) || bytes.Contains(sealed, []byte("sensitive input")) {
		t.Fatal("unsealed continuation", err)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("request", requestID)
	w = httptest.NewRecorder()
	aiPolicyRequest(c, w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var metadata map[string]any
	if json.Unmarshal(w.Body.Bytes(), &metadata) != nil {
		t.Fatal("request metadata")
	}
	claims := jwt.MapClaims{"v": 1, "jti": "approval-nonce", "iss": c.Config.AIPolicy.Issuer, "aud": c.Config.AIPolicy.Audience, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}
	for _, field := range []string{"request_id", "job_id", "chat_id", "source_user", "source_device", "ai_user", "ai_device", "epoch", "action", "name", "request_hash", "destination_hash"} {
		claims[field] = metadata[field]
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	approve := func() {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"token": token})
		w := httptest.NewRecorder()
		aiPolicyApprove(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(raw)))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	approve()
	approve()
	// Recreate the policy runtime over persisted state before resuming. Neither
	// a provider response nor a pending continuation lives only in the runner.
	c = &core.Core{DB: c.DB, Engine: c.Engine, Config: c.Config, Context: context.Background(), Mux: http.NewServeMux(), InTransaction: c.InTransaction, OnDelete: c.OnDelete}
	if err = installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	aiNamedProcess(c)
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id=?`, job).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("resume failed: %s %v", status, err)
	}
	approve()
	aiNamedProcess(c)
	if providerCalls.Load() != 2 || toolCalls.Load() != 1 || !noticeBeforeEffect.Load() {
		t.Fatalf("replayed or unjournaled effect: provider=%d tool=%d notice=%t", providerCalls.Load(), toolCalls.Load(), noticeBeforeEffect.Load())
	}
	var outputs, notices int
	_ = c.DB.QueryRow(`SELECT count(*) FROM messages WHERE sender='ai-one'`).Scan(&outputs)
	_ = c.DB.QueryRow(`SELECT count(*) FROM events WHERE kind='ai.egress.notice'`).Scan(&notices)
	if outputs != 1 || notices != 3 {
		t.Fatalf("outputs=%d notices=%d", outputs, notices)
	}
	w = httptest.NewRecorder()
	aiPolicyUsage(c, w, httptest.NewRequest("GET", "/?bot=ai-one", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var usage struct {
		Items []struct {
			Cost     int64  `json:"cost_microunits"`
			Reserved int64  `json:"reserved_microunits"`
			Status   string `json:"status"`
		}
	}
	if json.Unmarshal(w.Body.Bytes(), &usage) != nil || len(usage.Items) != 3 {
		t.Fatal("usage records", w.Body.String())
	}
	var cost, reserve int64
	for _, entry := range usage.Items {
		cost += entry.Cost
		reserve += entry.Reserved
	}
	if cost != 34 || reserve != 0 {
		t.Fatalf("incorrect settlement cost=%d reserved=%d: %s", cost, reserve, w.Body.String())
	}
	rows, err := c.DB.Query(`SELECT seq,data FROM events WHERE kind='ai.egress.notice'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var blob []byte
		if rows.Scan(&seq, &blob) != nil {
			t.Fatal("event")
		}
		plain, err := c.Engine.Open(blob, []byte(fmt.Sprintf("event/policy-chat/%d", seq)))
		if err != nil || bytes.Contains(plain, []byte("sensitive input")) || bytes.Contains(plain, []byte("test-marker")) || bytes.Contains(plain, []byte("private question")) {
			t.Fatal("notice leaks payload", err)
		}
	}
}

func TestAIPolicyCanonicalArgumentsAndEffectBinding(t *testing.T) {
	first, err := aiCanonicalArguments(json.RawMessage(`{"z":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := aiCanonicalArguments(json.RawMessage(`{"a":1, "z":2}`))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("unstable canonical arguments", err)
	}
	if _, err = aiCanonicalArguments(json.RawMessage(`{"a":1,"\u0061":2}`)); err == nil {
		t.Fatal("duplicate key accepted")
	}
	cfg := config.Defaults()
	state := aiContinuation{Epoch: 1, SessionVersion: 2}
	effect := makePolicyEffect("job", 0, 0, "tool", "lookup", "https://tools.example.test", first, true, 10, cfg.AI, state)
	state.SessionVersion++
	changed := makePolicyEffect("job", 0, 0, "tool", "lookup", "https://tools.example.test", first, true, 10, cfg.AI, state)
	if effect.RequestHash == changed.RequestHash {
		t.Fatal("session version not bound")
	}
}

func FuzzAIPolicyCanonicalArguments(f *testing.F) {
	for _, seed := range []string{`{"a":1,"b":[true,null]}`, `{"a":1,"a":2}`, `{"a":{"b":[]}}`, "null", `{"a":1e3}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 65536 {
			return
		}
		canonical, err := aiCanonicalArguments(json.RawMessage(raw))
		if err != nil {
			return
		}
		again, err := aiCanonicalArguments(canonical)
		if err != nil || !bytes.Equal(canonical, again) {
			t.Fatalf("non-idempotent canonicalization: %q %v", canonical, err)
		}
	})
}
