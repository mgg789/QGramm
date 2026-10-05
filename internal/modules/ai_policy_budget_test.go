//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/core"
)

type policyBudgetFixture struct {
	c       *core.Core
	job     string
	chat    string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func newPolicyBudgetFixture(t *testing.T) policyBudgetFixture {
	t.Helper()
	base, cancel := aiFixture(t)
	cancel()
	c := &core.Core{DB: base.DB, Engine: base.Engine, Config: base.Config, Context: context.Background(), Mux: http.NewServeMux(), InTransaction: base.InTransaction, OnDelete: base.OnDelete}
	c.Config.Features.AIPolicy = true
	c.Config.AIPolicy.ApprovalTTLSeconds = 300
	c.Config.AIPolicy.ProviderReserveMicrounits = 40
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(c.Config.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	if err = installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	f := policyBudgetFixture{c: c, job: "policy-job", chat: "policy-budget", public: public, private: private}
	if err = f.insertLegacyTask(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f policyBudgetFixture) insertLegacyTask() error {
	now := time.Now().Unix()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO users(id,disabled) VALUES('ai-budget',0)`, nil},
		{`INSERT INTO devices(id,user_id,public_key,signing_key,revoked) VALUES('ai-budget-device','ai-budget',?, '',0)`, []any{f.c.Engine.PublicKey()}},
		{`INSERT INTO chats(id,kind,mode,epoch,pending,created_at) VALUES(?, 'direct','basic',0,0,?)`, []any{f.chat, now}},
		{`INSERT INTO members(chat_id,user_id,role,can_send,joined_seq,active) VALUES(?, 'human','member',1,0,1),(?, 'ai-budget','member',1,0,1)`, []any{f.chat, f.chat}},
		{`INSERT INTO messages(id,chat_id,sender,device_id,operation_id,seq,revision,deleted,payload,metadata,epoch,created_at) VALUES('policy-source',?,'human','phone','policy-op',1,1,0,?,'{}',0,?)`, []any{f.chat, []byte("sealed-source"), now}},
		{`INSERT INTO ai_chats(chat_id,user_id,device_id,provider,tools,context,state) VALUES(?, 'ai-budget','ai-budget-device','openai','[]',?,X'')`, []any{f.chat, []byte("[]")}},
		{`INSERT INTO ai_jobs(id,chat_id,message_id,status,result_id,created_at,updated_at) VALUES(?,?,?,'running','',?,?)`, []any{f.job, f.chat, "policy-source", now, now}},
	}
	for _, stmt := range stmts {
		if _, err := f.c.DB.Exec(stmt.q, stmt.args...); err != nil {
			return err
		}
	}
	return nil
}

func (f policyBudgetFixture) effect(id string, reserve int64) aiPolicyEffect {
	return aiPolicyEffect{ID: id, Job: f.job, Action: "provider", Name: "openai", Destination: "https://provider.example/v1", Epoch: 0, SessionVersion: 0, ReserveMicrounits: reserve, InputPriceMicrounitsPerMillionTokens: 1_000_000, OutputPriceMicrounitsPerMillionTokens: 1_000_000}
}

func (f policyBudgetFixture) begin(t *testing.T, effect aiPolicyEffect) error {
	t.Helper()
	_, err := aiPolicyBegin(context.Background(), f.c, effect, []byte(`{"version":1,"turns":[]}`))
	return err
}

func openLedgerAmounts(t *testing.T, f policyBudgetFixture, request, scope string, blob []byte) aiPolicyAmounts {
	t.Helper()
	plain, err := f.c.Engine.Open(blob, []byte("ai-policy/amounts/"+request+"/"+scope))
	if err != nil {
		t.Fatal(err)
	}
	var amounts aiPolicyAmounts
	if err = json.Unmarshal(plain, &amounts); err != nil {
		t.Fatal(err)
	}
	return amounts
}

func requestStatus(t *testing.T, c *core.Core, id string) string {
	t.Helper()
	var status string
	if err := c.DB.QueryRow(`SELECT status FROM ai_policy_requests WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestAIPolicyBudgetAdmissionIsAtomicAcrossGlobalBotAndSource(t *testing.T) {
	for _, tc := range []struct {
		name                string
		global, bot, source int64
	}{
		{"global", 50, 100, 100},
		{"bot", 100, 50, 100},
		{"source", 100, 100, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPolicyBudgetFixture(t)
			f.c.Config.AIPolicy.GlobalDailyBudgetMicrounits = tc.global
			f.c.Config.AIPolicy.PerBotDailyBudgetMicrounits = tc.bot
			f.c.Config.AIPolicy.PerUserDailyBudgetMicrounits = tc.source
			if err := f.begin(t, f.effect("budget-first", 40)); err != nil {
				t.Fatal(err)
			}
			if err := f.begin(t, f.effect("budget-second", 20)); err == nil {
				t.Fatal("budget admission accepted an over-cap reservation")
			}
			var count int
			if err := f.c.DB.QueryRow(`SELECT count(*) FROM ai_policy_ledger`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 3 {
				t.Fatalf("failed admission partially reserved scopes: %d ledger rows", count)
			}
		})
	}
}

func TestAIPolicyConcurrentBudgetAdmissionDoesNotOversubscribe(t *testing.T) {
	f := newPolicyBudgetFixture(t)
	f.c.Config.AIPolicy.GlobalDailyBudgetMicrounits = 100
	f.c.Config.AIPolicy.PerBotDailyBudgetMicrounits = 100
	f.c.Config.AIPolicy.PerUserDailyBudgetMicrounits = 100
	results := make(chan error, 4)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- f.begin(t, f.effect("concurrent-"+string(rune('a'+i)), 30))
		}(i)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 3 {
		t.Fatalf("concurrent admission accepted %d reservations, want 3", succeeded)
	}
	var count int
	if err := f.c.DB.QueryRow(`SELECT count(*) FROM ai_policy_ledger`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("unexpected partially admitted scope rows: %d", count)
	}
}

func TestAIPolicySettlementKnownUnknownAndDuplicateIsIdempotent(t *testing.T) {
	f := newPolicyBudgetFixture(t)
	f.c.Config.AIPolicy.GlobalDailyBudgetMicrounits = 1000
	f.c.Config.AIPolicy.PerBotDailyBudgetMicrounits = 1000
	f.c.Config.AIPolicy.PerUserDailyBudgetMicrounits = 1000
	known := f.effect("settle-known", 40)
	if err := f.begin(t, known); err != nil {
		t.Fatal(err)
	}
	usage := aiUsage{InputTokens: 2, OutputTokens: 3, CacheReadTokens: 1, CacheWriteTokens: 1, Known: true}
	if err := aiPolicySettle(context.Background(), f.c, known.ID, usage, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := aiPolicySettle(context.Background(), f.c, known.ID, usage, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, f.c, known.ID); got != "completed" {
		t.Fatalf("known settlement status %s", got)
	}
	rows, err := f.c.DB.Query(`SELECT request_id,scope,amounts FROM ai_policy_ledger WHERE request_id=?`, known.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for rows.Next() {
		var request, scope string
		var blob []byte
		if err = rows.Scan(&request, &scope, &blob); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		amounts := openLedgerAmounts(t, f, request, scope, blob)
		if amounts.Reserved != 0 || amounts.Cost != 5 || !amounts.Known || amounts.InputTokens != 2 || amounts.OutputTokens != 3 || amounts.CacheReadTokens != 1 || amounts.CacheWriteTokens != 1 {
			rows.Close()
			t.Fatalf("unexpected known ledger amounts: %+v", amounts)
		}
		seen++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("expected 3 scope ledger rows, got %d", seen)
	}

	unknown := f.effect("settle-unknown", 40)
	if err = f.begin(t, unknown); err != nil {
		t.Fatal(err)
	}
	if err = aiPolicySettle(context.Background(), f.c, unknown.ID, aiUsage{}, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, f.c, unknown.ID); got != "uncertain" {
		t.Fatalf("unknown settlement status %s", got)
	}
	var blob []byte
	if err = f.c.DB.QueryRow(`SELECT amounts FROM ai_policy_ledger WHERE request_id=? AND scope='global'`, unknown.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	amounts := openLedgerAmounts(t, f, unknown.ID, "global", blob)
	if amounts.Reserved != 40 || amounts.Cost != 0 || amounts.Known {
		t.Fatalf("unknown usage did not retain reservation: %+v", amounts)
	}
	if err = aiPolicySettle(context.Background(), f.c, unknown.ID, aiUsage{InputTokens: 1, OutputTokens: 1, Known: true}, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err = aiPolicySettle(context.Background(), f.c, unknown.ID, aiUsage{InputTokens: 1, OutputTokens: 1, Known: true}, "succeeded"); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, f.c, unknown.ID); got != "completed" {
		t.Fatalf("known retry settlement status %s", got)
	}
}

func TestAIPolicyRevocationBeforeConsumeRejectsApproval(t *testing.T) {
	f := newPolicyBudgetFixture(t)
	effect := f.effect("revoked-request", 40)
	effect.Required = true
	effect.RequestHash = aiPolicyEffectHash(effect)
	if _, err := aiPolicyBegin(context.Background(), f.c, effect, []byte(`{"version":1}`)); err != ErrAIApprovalPending {
		t.Fatalf("begin error = %v", err)
	}
	var job, chat, sourceUser, sourceDevice, aiUser, aiDevice, hash, action, name, destinationHash string
	var epoch int64
	if err := f.c.DB.QueryRow(`SELECT job_id,chat_id,source_user,source_device,ai_user,ai_device,epoch,request_hash,action,name,destination_hash FROM ai_policy_requests WHERE id=?`, effect.ID).Scan(&job, &chat, &sourceUser, &sourceDevice, &aiUser, &aiDevice, &epoch, &hash, &action, &name, &destinationHash); err != nil {
		t.Fatal(err)
	}
	revoke := httptest.NewRequest("DELETE", "/management/v1/ai/grants/revoked-nonce", nil)
	revoke.SetPathValue("grant", "revoked-nonce")
	w := httptest.NewRecorder()
	aiPolicyRevoke(f.c, w, revoke)
	if w.Code != 200 {
		t.Fatalf("revoke-before-issue status %d: %s", w.Code, w.Body.String())
	}
	claims := jwt.MapClaims{"v": 1, "jti": "revoked-nonce", "request_id": effect.ID, "job_id": job, "chat_id": chat, "source_user": sourceUser, "source_device": sourceDevice, "ai_user": aiUser, "ai_device": aiDevice, "epoch": epoch, "action": action, "name": name, "request_hash": hash, "destination_hash": destinationHash, "iss": f.c.Config.AIPolicy.Issuer, "aud": f.c.Config.AIPolicy.Audience, "iat": time.Now().Unix() - 1, "exp": time.Now().Add(time.Minute).Unix()}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(f.private)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token})
	w = httptest.NewRecorder()
	aiPolicyApprove(f.c, w, httptest.NewRequest("POST", "/management/v1/ai/approvals", bytes.NewReader(body)))
	if w.Code != 409 {
		t.Fatalf("revoked approval status %d: %s", w.Code, w.Body.String())
	}
	var grants int
	if err = f.c.DB.QueryRow(`SELECT count(*) FROM ai_policy_grants WHERE request_id=?`, effect.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("revoked token was stored as grant: %d", grants)
	}
}

func TestAIPolicyRestartMarksDispatchedUncertainAndPreventsReplay(t *testing.T) {
	f := newPolicyBudgetFixture(t)
	f.c.Config.AIPolicy.GlobalDailyBudgetMicrounits = 1000
	f.c.Config.AIPolicy.PerBotDailyBudgetMicrounits = 1000
	f.c.Config.AIPolicy.PerUserDailyBudgetMicrounits = 1000
	effect := f.effect("restart-request", 40)
	if err := f.begin(t, effect); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, f.c, effect.ID); got != "dispatched" {
		t.Fatalf("before restart status %s", got)
	}
	restarted := &core.Core{DB: f.c.DB, Engine: f.c.Engine, Config: f.c.Config, Context: context.Background(), Mux: http.NewServeMux()}
	if err := installAIPolicy(restarted); err != nil {
		t.Fatal(err)
	}
	f.c = restarted
	if got := requestStatus(t, f.c, effect.ID); got != "uncertain" {
		t.Fatalf("after restart status %s", got)
	}
	var jobStatus string
	if err := f.c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id=?`, f.job).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "uncertain" {
		t.Fatalf("after restart job status %s", jobStatus)
	}
	var blob []byte
	if err := f.c.DB.QueryRow(`SELECT amounts FROM ai_policy_ledger WHERE request_id=? AND scope='global'`, effect.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if got := openLedgerAmounts(t, f, effect.ID, "global", blob).Reserved; got != 40 {
		t.Fatalf("restart released reservation: %d", got)
	}
	if err := f.begin(t, effect); err == nil {
		t.Fatal("dispatched effect was replayed after restart")
	}
}
