//go:build qg_ai_storage && qg_ai_policy && qg_openai && qg_http_tools

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
	"github.com/mgg789/QGramm/internal/aivault"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestAIStorageSignedRetrievalAndContextIsolation(t *testing.T) {
	for _, mode := range []string{"success", "missing-storage-grant", "revoked-before-provider"} {
		t.Run(mode, func(t *testing.T) { testAIStorageWorkflow(t, mode) })
	}
}
func testAIStorageWorkflow(t *testing.T, mode string) {
	base, cancel := aiFixture(t)
	cancel()
	c := &core.Core{DB: base.DB, Engine: base.Engine, Config: base.Config, Context: context.Background(), Mux: http.NewServeMux(), InTransaction: base.InTransaction, OnDelete: base.OnDelete}
	c.Config.Features.AIPolicy = true
	c.Config.Features.AIStorage = true
	c.Config.AIPolicy.ProviderReserveMicrounits = 100
	c.Config.AIPolicy.GlobalDailyBudgetMicrounits = 1000
	c.Config.AI.InputPriceMicrounitsPerMillionTokens = 1000000
	c.Config.AI.OutputPriceMicrounitsPerMillionTokens = 2000000
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(c.Config.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(c.Config.AIStorage.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	if err := installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	if err := installAIStorage(c); err != nil {
		t.Fatal(err)
	}
	state, _ := vaultState(c)
	if _, err := state.Store.CreateResource(context.Background(), aivault.ResourceSpec{ID: "private-data", Scope: aivault.ScopeUser, Owner: "human"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Store.PutDocument(context.Background(), "private-data", aivault.DocumentInput{ID: "doc", Text: "vault secret fact"}); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := providerCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if count == 1 {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"knowledge-1","function":{"name":"knowledge","arguments":"{\"query\":\"vault secret\"}"}}]}}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`)
			return
		}
		var body struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) < 3 || body.Messages[len(body.Messages)-1].Role != "tool" || !strings.Contains(body.Messages[len(body.Messages)-1].Content, "vault secret fact") {
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
	c.Config.AI.Bots["ai-one"] = config.AIBot{Endpoint: "named-test", Tools: []string{"knowledge"}}
	c.Config.AI.Tools = []config.Tool{{Name: "knowledge", Kind: "storage", Resource: "private-data", StorageAction: "search", RequireApproval: true, Schema: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}, "additionalProperties": false}}}
	w := httptest.NewRecorder()
	aiCreateNamedAgent(c, w, httptest.NewRequest("POST", "/", strings.NewReader(`{"bot_name":"ai-one","user_id":"ai-one"}`)))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	addNamedChatFixture(t, c, "policy-chat", "ai-one")
	if _, err := c.DB.Exec(`UPDATE chats SET kind='direct' WHERE id='policy-chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB.Exec(`UPDATE ai_sessions SET tools='["knowledge"]' WHERE chat_id='policy-chat'`); err != nil {
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
	if providerCalls.Load() != 1 {
		t.Fatal("unapproved effect executed")
	}
	if err = c.DB.QueryRow(`SELECT id FROM ai_policy_requests WHERE status='awaiting'`).Scan(&requestID); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err = c.DB.QueryRow(`SELECT payload FROM ai_policy_requests WHERE id=?`, requestID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("private question")) || bytes.Contains(sealed, []byte("vault secret")) {
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
	// An expired policy request cannot mint a fresh storage capability.
	if _, err := c.DB.Exec(`UPDATE ai_policy_requests SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).Unix(), requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := vaultRequestBinding(context.Background(), c, requestID); err == nil {
		t.Fatal("grant issued for expired policy request")
	}
	if _, err := c.DB.Exec(`UPDATE ai_policy_requests SET expires_at=? WHERE id=?`, time.Now().Add(time.Minute).Unix(), requestID); err != nil {
		t.Fatal(err)
	}
	approve()
	// Policy approval alone cannot release vault content; issue the separate
	// signed resource/destination grant while the exact request is still known.
	binding, err := vaultRequestBinding(context.Background(), c, requestID)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := aivault.NewGrantSigner(private, c.Config.AIStorage.Issuer, c.Config.AIStorage.Audience, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := signer.Sign(aivault.Claims{Nonce: "vault-grant", RequestID: requestID, RequestHash: binding.Hash, ChatID: binding.Task.Chat, SourceUser: binding.Task.SourceUser, AIUser: binding.Task.User, Resource: binding.Tool.Resource, Action: aivault.ActionSearch, Destination: binding.Destination, ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if mode == "missing-storage-grant" {
		aiNamedProcess(c)
		if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id=?`, job).Scan(&status); err != nil || status != "awaiting_approval" || providerCalls.Load() != 1 {
			t.Fatalf("ungranted retrieval status=%s provider=%d err=%v", status, providerCalls.Load(), err)
		}
		// Continue after the second permission arrives, without another paid call.
	}
	grantRaw, _ := json.Marshal(map[string]string{"token": grant})
	w = httptest.NewRecorder()
	aiVaultGrant(c, w, httptest.NewRequest("POST", "/", bytes.NewReader(grantRaw)))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if mode == "revoked-before-provider" {
		tx, e := c.DB.BeginTx(context.Background(), nil)
		if e != nil {
			t.Fatal(e)
		}
		task, e := aiTaskLookup(context.Background(), tx, job)
		_ = tx.Rollback()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = c.DB.Exec(`UPDATE ai_tasks SET status='running' WHERE id=?`, job); e != nil {
			t.Fatal(e)
		}
		if _, e = c.DB.Exec(`UPDATE ai_policy_requests SET status='dispatched' WHERE id=?`, requestID); e != nil {
			t.Fatal(e)
		}
		var boundary *aiStorageBoundary
		settings, _, e := c.Config.ResolveBot("ai-one")
		if e != nil {
			t.Fatal(e)
		}
		ctx := context.WithValue(context.Background(), aiSettingsKey{}, settings)
		ctx = context.WithValue(ctx, aiVaultRuntimeKey{}, aiVaultRuntime{Core: c, Job: job, Boundary: &boundary})
		ctx = context.WithValue(ctx, aiPolicyTicketKey{}, requestID)
		if _, e = aiVaultTool(ctx, settings.Tools[0], json.RawMessage(`{"query":"vault secret"}`), nil); e != nil {
			t.Fatal(e)
		}
		if boundary == nil || task.SourceUser != "human" {
			t.Fatal("retrieval boundary absent")
		}
		if _, e = c.DB.Exec(`UPDATE ai_storage_grants SET revoked=1`); e != nil {
			t.Fatal(e)
		}
		if e = aiVaultRelease(context.Background(), c, job, boundary, "provider", binding.Destination); e == nil {
			t.Fatal("revoked retrieval released to provider")
		}
		tx, e = c.DB.BeginTx(context.Background(), nil)
		if e != nil {
			t.Fatal(e)
		}
		e = aiVaultValidateFinal(context.Background(), tx, c, job)
		_ = tx.Rollback()
		if e == nil {
			t.Fatal("revoked retrieval final output accepted")
		}
		if providerCalls.Load() != 1 {
			t.Fatal("unexpected external effect")
		}
		return
	}
	aiNamedProcess(c)
	if err = c.DB.QueryRow(`SELECT status FROM ai_tasks WHERE id=?`, job).Scan(&status); err != nil || status != "succeeded" {
		t.Fatalf("retrieval failed: %s %v", status, err)
	}
	if providerCalls.Load() != 2 {
		t.Fatal("unexpected provider replay", providerCalls.Load())
	}
	var sealedContext []byte
	if err = c.DB.QueryRow(`SELECT context FROM ai_sessions WHERE chat_id='policy-chat'`).Scan(&sealedContext); err != nil {
		t.Fatal(err)
	}
	var sessionID string
	var version int64
	if err = c.DB.QueryRow(`SELECT agent_id,version FROM ai_sessions WHERE chat_id='policy-chat'`).Scan(&sessionID, &version); err != nil {
		t.Fatal(err)
	}
	plain, err := c.Engine.Open(sealedContext, []byte(fmt.Sprintf("ai/context/policy-chat/%s/%d", sessionID, version)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("vault secret")) || bytes.Contains(plain, []byte("final answer")) {
		t.Fatal("retrieved private context retained across requests")
	}
	boundary := &aiStorageBoundary{Label: "user/human", Destination: binding.Destination, Requests: []string{requestID}}
	if err = aiVaultRelease(context.Background(), c, job, boundary, "http", "https://other.example"); err == nil {
		t.Fatal("private context escaped to another tool")
	}
	if err = aiVaultRelease(context.Background(), c, job, boundary, "provider", "https://other.example"); err == nil {
		t.Fatal("private context escaped to another provider")
	}
	// Altering the audience or room shape cannot widen private storage scope.
	tx, err := c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := aiTaskLookup(context.Background(), tx, job)
	if err != nil {
		t.Fatal(err)
	}
	wrong := task
	wrong.SourceUser = "another-user"
	if _, err = vaultResourceAuthorized(context.Background(), tx, wrong, "private-data"); err == nil {
		t.Fatal("cross-user private access")
	}
	_ = tx.Rollback()
	if _, err = c.DB.Exec(`UPDATE chats SET kind='group' WHERE id='policy-chat'`); err != nil {
		t.Fatal(err)
	}
	tx, err = c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = vaultResourceAuthorized(context.Background(), tx, task, "private-data"); err == nil {
		t.Fatal("private data entered group")
	}
	_ = tx.Rollback()
	if _, err = c.DB.Exec(`UPDATE chats SET kind='direct' WHERE id='policy-chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DB.Exec(`UPDATE ai_storage_grants SET revoked=1`); err != nil {
		t.Fatal(err)
	}
	if err = aiVaultRelease(context.Background(), c, job, boundary, "provider", binding.Destination); err == nil {
		t.Fatal("revoked grant accepted")
	}
}
