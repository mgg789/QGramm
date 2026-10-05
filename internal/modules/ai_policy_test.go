//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"math"
	"testing"
	"time"
)

func TestAIPolicyOriginStripsPathQueryAndUserinfo(t *testing.T) {
	if got := aiPolicyOrigin("https://user:secret@example.test/private?prompt=secret"); got != "invalid" {
		t.Fatalf("userinfo must be rejected, got %q", got)
	}
	if got := aiPolicyOrigin("https://example.test/private?prompt=secret"); got != "https://example.test" {
		t.Fatalf("origin leaked path/query: %q", got)
	}
	if got := aiPolicyOrigin("not a URL"); got != "invalid" {
		t.Fatalf("invalid URL accepted: %q", got)
	}
}

func TestAIPolicyCostRoundsPerTokenClassAndRejectsOverflow(t *testing.T) {
	effect := aiPolicyEffect{InputPriceMicrounitsPerMillionTokens: 1_000_001, OutputPriceMicrounitsPerMillionTokens: 2_000_000}
	cost, err := aiPolicyCost(effect, aiUsage{Known: true, InputTokens: 1, OutputTokens: 1})
	if err != nil || cost != 4 {
		t.Fatalf("unexpected rounded cost %d, %v", cost, err)
	}
	if _, err = aiPolicyCost(effect, aiUsage{Known: true, InputTokens: math.MaxInt64, OutputTokens: 0}); err == nil {
		t.Fatal("overflowing token count was accepted")
	}
}

func TestAIPolicyStrictJWTJSONRejectsUnknownAndDuplicateClaims(t *testing.T) {
	encode := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	var out struct {
		V int `json:"v"`
	}
	if err := strictJSON(encode(`{"v":1,"unknown":true}`), &out); err == nil {
		t.Fatal("unknown claim was accepted")
	}
	if err := strictJSON(encode(`{"v":1,"v":1}`), &out); err == nil {
		t.Fatal("duplicate claim was accepted")
	}
	if err := strictJSON(encode(`{"v":1}`), &out); err != nil || out.V != 1 {
		t.Fatalf("valid claim rejected: %v", err)
	}
}

func TestAIPolicyExpiryScrubsContinuationAndUnblocksJob(t *testing.T) {
	c, _ := aiFixture(t)
	c.Config.Features.AIPolicy = true
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyEnv := c.Config.AIPolicy.GrantPublicKeyEnv
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(pub))
	if err = installAIPolicy(c); err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO chats(id,kind,mode,created_at) VALUES('expiry-chat','direct','basic',?)`, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	job, request := "expiry-job", "expiry-request"
	effect := aiPolicyEffect{ID: request, Job: job, Action: "provider", Name: "openai", Destination: "https://provider.example/v1", Epoch: 0}
	sealed, err := c.Engine.Seal(mustJSON(aiPolicyStored{Effect: effect, Continuation: []byte(`{"secret":"prompt"}`), SessionEpoch: 0}), aiPolicyAAD("request", request))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	_, err = c.DB.Exec(`INSERT INTO ai_jobs(id,chat_id,message_id,status,created_at,updated_at) VALUES(?,?,?,'awaiting_approval',?,?)`, job, "expiry-chat", "missing-message", now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.DB.Exec(`INSERT INTO ai_policy_requests(id,job_id,chat_id,source_user,source_device,ai_user,ai_device,epoch,request_hash,action,name,destination_hash,payload,status,expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'awaiting',?,?,?)`, request, job, "expiry-chat", "human", "phone", "ai", "ai-device", 0, aiPolicyEffectHash(effect), effect.Action, effect.Name, aiPolicyDestinationHash(effect.Destination), sealed, now-120, now-180, now-180)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Cleanup) == 0 {
		t.Fatal("policy cleanup was not installed")
	}
	if err = c.Cleanup[len(c.Cleanup)-1](context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, jobStatus string
	var scrubbed []byte
	if err = c.DB.QueryRow(`SELECT status,payload FROM ai_policy_requests WHERE id=?`, request).Scan(&status, &scrubbed); err != nil {
		t.Fatal(err)
	}
	if status != "expired" || string(scrubbed) == "" {
		t.Fatalf("unexpected expired request: %s", status)
	}
	if err = c.DB.QueryRow(`SELECT status FROM ai_jobs WHERE id=?`, job).Scan(&jobStatus); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "failed" {
		t.Fatalf("job remained blocked: %s", jobStatus)
	}
	stored, err := aiPolicyOpenStored(c, request, scrubbed)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Continuation) != 0 {
		t.Fatal("expired continuation was retained")
	}
}
