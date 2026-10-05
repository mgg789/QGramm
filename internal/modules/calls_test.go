//go:build qg_calls

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestTURNHMACVector(t *testing.T) {
	const expected = "iyZkHzHakdkQ4e1C19e4QYZVluU="
	if got := turnCredential("shared-secret", "1700000000:alice"); got != expected {
		t.Fatalf("credential %q, want %q", got, expected)
	}
	if turnCredential("different-secret", "1700000000:alice") == expected {
		t.Fatal("secret does not bind credential")
	}
}

func TestCallsHTTPEncryptedSignalingAndState(t *testing.T) {
	public, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.Defaults()
	cfg.Server.AllowInsecureLoopback = true
	cfg.Storage.Path = filepath.Join(t.TempDir(), "calls.db")
	cfg.Storage.Files = t.TempDir()
	cfg.Security.TokenPublicKeyEnv = "CALL_TEST_TOKEN"
	cfg.Security.MasterKeyEnv = "CALL_TEST_MASTER"
	cfg.Security.HPKEKeyEnv = "CALL_TEST_HPKE"
	cfg.Security.ManagementSecretEnv = "CALL_TEST_MANAGEMENT"
	cfg.Calls.TURNSecretEnv = "CALL_TEST_TURN"
	cfg.Calls.TURNURLs = []string{"turn:127.0.0.1:3478"}
	cfg.AI.Model = "test"
	cfg.AI.OpenAIKeyEnv = "CALL_TEST_AI"
	cfg.AI.AnthropicKeyEnv = "CALL_TEST_AI"
	cfg.AIPolicy.GrantPublicKeyEnv = "CALL_TEST_AI_GRANT"
	t.Setenv(cfg.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.Security.TokenPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.Security.MasterKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv(cfg.Security.HPKEKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))
	t.Setenv(cfg.Security.ManagementSecretEnv, strings.Repeat("m", 32))
	t.Setenv(cfg.Calls.TURNSecretEnv, "test-secret")
	// Match the actual tagged artifact, including other optional modules compiled by CI.
	fields := reflect.ValueOf(&cfg.Features).Elem()
	for _, name := range core.Registered() {
		for i := 0; i < fields.NumField(); i++ {
			if strings.EqualFold(fields.Type().Field(i).Name, strings.ReplaceAll(name, "_", "")) {
				fields.Field(i).SetBool(true)
			}
		}
	}
	c, e := core.Open(cfg, core.Registered())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	peer, e := cryptoenc.New(bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 32))
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.DB.Exec(`INSERT INTO users(id) VALUES('alice'),('bob'),('mallory'); INSERT INTO devices(id,user_id,public_key) VALUES('alice-phone','alice',?),('bob-phone','bob',?),('mallory-phone','mallory',?); INSERT INTO chats(id,kind,mode,created_at) VALUES('call-chat','direct','basic',0); INSERT INTO members(chat_id,user_id,role,can_send,joined_seq) VALUES('call-chat','alice','member',1,0),('call-chat','bob','member',1,0)`, peer.PublicKey(), peer.PublicKey(), peer.PublicKey())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(c.Mux)
	t.Cleanup(server.Close)
	request := func(user, path string, body any) (int, []byte) {
		t.Helper()
		claims := jwt.MapClaims{"sub": user, "device_id": user + "-phone", "iss": cfg.Security.Issuer, "aud": cfg.Security.Audience, "iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix()}
		token, e := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(private)
		if e != nil {
			t.Fatal(e)
		}
		wire, _ := json.Marshal(body)
		r, _ := http.NewRequest("POST", server.URL+path, bytes.NewReader(wire))
		r.Header.Set("Authorization", "Bearer "+token)
		res, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		out, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, out
	}
	code, out := request("alice", "/v1/chats/call-chat/calls", map[string]string{"mode": "video"})
	if code != 201 {
		t.Fatalf("call create %d %s", code, out)
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(out, &created)
	path := "/v1/calls/" + created.ID + "/signals"
	if code, out = request("mallory", path, map[string]string{"type": "end", "operation_id": "foreign"}); code != 403 {
		t.Fatalf("foreign signaling %d %s", code, out)
	}
	if code, _ = request("alice", path, map[string]string{"type": "accept", "operation_id": "wrong-accept"}); code != 409 {
		t.Fatalf("caller accepts %d", code)
	}
	pk, _ := base64.StdEncoding.DecodeString(c.Engine.PublicKey())
	secretSDP := "v=0\r\na=fingerprint:sha-256 private-fingerprint"
	payload, _ := json.Marshal(map[string]string{"sdp": secretSDP})
	env, e := cryptoenc.SealEnvelope(pk, payload, cryptoenc.Binding("call-chat", "alice", "alice-phone", "offer-1"))
	if e != nil {
		t.Fatal(e)
	}
	offer := map[string]any{"type": "offer", "operation_id": "offer-1", "to_device": "bob-phone", "envelope": env}
	if code, out = request("alice", path, offer); code != 200 {
		t.Fatalf("encrypted offer %d %s", code, out)
	}
	first := string(out)
	if code, out = request("alice", path, offer); code != 200 || string(out) != first {
		t.Fatalf("duplicate offer %d %s", code, out)
	}
	offer["to_device"] = "mallory-phone"
	if code, _ = request("alice", path, offer); code != 409 {
		t.Fatalf("operation mutation accepted %d", code)
	}
	offer["operation_id"] = "foreign-target"
	env, _ = cryptoenc.SealEnvelope(pk, payload, cryptoenc.Binding("call-chat", "alice", "alice-phone", "foreign-target"))
	offer["envelope"] = env
	if code, _ = request("alice", path, offer); code != 403 {
		t.Fatalf("foreign recipient accepted %d", code)
	}
	events, e := c.Events(context.Background(), core.Identity{UserID: "bob", DeviceID: "bob-phone"}, "call-chat", 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, event := range events {
		if event.Type != "call.offer" {
			continue
		}
		wire, _ := json.Marshal(event.Data)
		if bytes.Contains(wire, []byte("private-fingerprint")) {
			t.Fatal("event exposes plaintext signaling")
		}
		var data struct {
			Envelope cryptoenc.Envelope `json:"envelope"`
		}
		json.Unmarshal(wire, &data)
		plain, e := peer.OpenEnvelope(data.Envelope, cryptoenc.Binding("call-chat", "bob", "bob-phone", "offer-1"))
		var recovered struct {
			SDP string `json:"sdp"`
		}
		parseErr := json.Unmarshal(plain, &recovered)
		if e != nil || parseErr != nil || recovered.SDP != secretSDP {
			t.Fatalf("recipient decrypt %v %s", e, plain)
		}
		found = true
	}
	if !found {
		t.Fatal("offer missing from durable events")
	}
	if code, out = request("bob", path, map[string]string{"type": "accept", "operation_id": "accept-1"}); code != 200 {
		t.Fatalf("callee accepts %d %s", code, out)
	}
	if code, _ = request("alice", path, map[string]any{"type": "ice", "operation_id": "plaintext", "candidate": "candidate:private"}); code != 400 {
		t.Fatalf("plaintext ICE accepted %d", code)
	}
	if _, e = c.DB.Exec(`UPDATE chats SET mode='e2ee',epoch=2 WHERE id='call-chat'`); e != nil {
		t.Fatal(e)
	}
	opaque := base64.StdEncoding.EncodeToString([]byte("opaque MLS signaling"))
	if code, _ = request("alice", path, map[string]any{"type": "ice", "operation_id": "old-epoch", "mls": opaque, "epoch": 1}); code != 409 {
		t.Fatalf("stale epoch %d", code)
	}
	if code, out = request("alice", path, map[string]any{"type": "ice", "operation_id": "new-epoch", "mls": opaque, "epoch": 2}); code != 200 {
		t.Fatalf("opaque MLS %d %s", code, out)
	}
	if _, e = c.DB.Exec(`UPDATE chats SET pending=1 WHERE id='call-chat'`); e != nil {
		t.Fatal(e)
	}
	if code, _ = request("alice", path, map[string]any{"type": "ice", "operation_id": "pending", "mls": opaque, "epoch": 2}); code != 409 {
		t.Fatalf("pending epoch accepted %d", code)
	}
	if code, _ = request("alice", path, map[string]string{"type": "end", "operation_id": "end-1"}); code != 200 {
		t.Fatalf("end %d", code)
	}
	if code, _ = request("alice", path, map[string]string{"type": "end", "operation_id": "end-2"}); code != 409 {
		t.Fatalf("repeated non-idempotent end %d", code)
	}
	code, out = request("alice", "/v1/chats/call-chat/calls", map[string]string{"mode": "audio"})
	if code != 201 {
		t.Fatalf("call after end %d %s", code, out)
	}
	var abandoned struct {
		ID string `json:"id"`
	}
	json.Unmarshal(out, &abandoned)
	if _, e = c.DB.Exec(`UPDATE calls SET created_at=? WHERE id=?`, time.Now().Unix()-121, abandoned.ID); e != nil {
		t.Fatal(e)
	}
	for _, cleanup := range c.Cleanup {
		if e = cleanup(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	var state string
	if e = c.DB.QueryRow(`SELECT state FROM calls WHERE id=?`, abandoned.ID).Scan(&state); e != nil || state != "ended" {
		t.Fatalf("abandoned call state %q %v", state, e)
	}
	if _, e = c.DB.Exec(`UPDATE call_operations SET created_at=?`, time.Now().Unix()-int64(cfg.Policy.DedupRetentionHours)*3600-1); e != nil {
		t.Fatal(e)
	}
	for _, cleanup := range c.Cleanup {
		if e = cleanup(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	var operations int
	if e = c.DB.QueryRow(`SELECT count(*) FROM call_operations`).Scan(&operations); e != nil || operations != 0 {
		t.Fatalf("operation TTL cleanup %d %v", operations, e)
	}
	if _, e = c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='alice-phone'`); e != nil {
		t.Fatal(e)
	}
	if code, _ = request("alice", path, map[string]string{"type": "end", "operation_id": "end-1"}); code != 401 {
		t.Fatalf("revoked device repeat accepted %d", code)
	}
}
