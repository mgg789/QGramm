package core

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	c          *Core
	cfg        config.Config
	sign       ed25519.PrivateKey
	alice, bob *cryptoenc.Engine
	id         Identity
	server     *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.AllowInsecureLoopback = true
	cfg.Capacity = config.DeriveCapacity(cfg.Capacity, config.DetectResources())
	cfg.Storage.Path = filepath.Join(t.TempDir(), "qgramm.db")
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(cfg.Security.TokenPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.Security.ManagementSecretEnv, strings.Repeat("m", 32))
	for _, name := range []string{cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		key := make([]byte, 32)
		rand.Read(key)
		t.Setenv(name, base64.StdEncoding.EncodeToString(key))
	}
	c, err := Open(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := func() *cryptoenc.Engine {
		key := make([]byte, 32)
		rand.Read(key)
		e, err := cryptoenc.New(bytes.Repeat([]byte{1}, 32), key)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	f := &fixture{c: c, cfg: cfg, sign: private, alice: engine(), bob: engine(), id: Identity{"alice", "alice-phone"}}
	f.server = httptest.NewServer(c.Handler())
	t.Cleanup(func() { f.server.Close(); c.Close() })
	for _, u := range []string{"alice", "bob"} {
		f.require(t, "PUT", "/management/v1/users/"+u, map[string]bool{"disabled": false}, 200, true)
	}
	for _, entry := range []struct {
		user, device string
		e            *cryptoenc.Engine
	}{{"alice", "alice-phone", f.alice}, {"bob", "bob-phone", f.bob}} {
		f.require(t, "PUT", "/management/v1/users/"+entry.user+"/devices/"+entry.device, map[string]string{"public_key": entry.e.PublicKey()}, 200, true)
	}
	f.require(t, "POST", "/management/v1/chats/direct", map[string]any{"id": "chat", "members": []string{"alice", "bob"}}, 201, true)
	return f
}
func (f *fixture) token(id Identity, issuer string) string {
	now := time.Now()
	claims := jwt.MapClaims{"sub": id.UserID, "device_id": id.DeviceID, "iss": issuer, "aud": f.cfg.Security.Audience, "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(f.sign)
	return token
}
func (f *fixture) require(t *testing.T, method, path string, body any, status int, management bool) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if management {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("m", 32))
	} else {
		r.Header.Set("Authorization", "Bearer "+f.token(f.id, f.cfg.Security.Issuer))
	}
	w := httptest.NewRecorder()
	f.c.Handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s => %d wanted %d: %s", method, path, w.Code, status, w.Body)
	}
	return w.Body.Bytes()
}
func (f *fixture) input(t *testing.T, op, plain string) MessageInput {
	t.Helper()
	public, _ := base64.StdEncoding.DecodeString(f.c.Engine.PublicKey())
	env, err := cryptoenc.SealEnvelope(public, []byte(plain), cryptoenc.Binding("chat", f.id.UserID, f.id.DeviceID, op))
	if err != nil {
		t.Fatal(err)
	}
	return MessageInput{OperationID: op, Envelope: &env}
}
func TestDurableSendDedupAndReopen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in := f.input(t, "op1", "sensitive message")
	m, err := f.c.Send(ctx, f.id, "chat", in)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := f.c.Send(ctx, f.id, "chat", in)
	if err != nil || repeated.ID != m.ID {
		t.Fatal("dedup failed", err)
	}
	changed := f.input(t, "op1", "changed")
	if _, err = f.c.Send(ctx, f.id, "chat", changed); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	bob := Identity{"bob", "bob-phone"}
	view, err := f.c.ViewMessage(ctx, bob, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.bob.OpenEnvelope(*view.Envelope, cryptoenc.Binding("chat", "bob", "bob-phone", m.ID))
	if err != nil || string(plain) != "sensitive message" {
		t.Fatal("recipient envelope failed", err)
	}
	var stored []byte
	_ = f.c.DB.QueryRow(`SELECT payload FROM messages WHERE id=?`, m.ID).Scan(&stored)
	if bytes.Contains(stored, plain) {
		t.Fatal("plaintext persisted")
	}
	events, err := f.c.Events(ctx, bob, "chat", 0, 100)
	if err != nil || len(events) != 1 || events[0].Seq != 1 {
		t.Fatal("replay failed", events, err)
	}
	if _, err = f.c.Events(ctx, Identity{"mallory", "unknown"}, "chat", 0, 100); err == nil {
		t.Fatal("foreign history visible")
	}
	if err = f.c.Close(); err != nil {
		t.Fatal(err)
	}
	f.c, err = Open(f.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.c.Close() })
	if _, err = f.c.ViewMessage(ctx, bob, m.ID); err != nil {
		t.Fatal("restart lost message", err)
	}
}
func TestAuthRevocationReceiptsAndExpiredCursor(t *testing.T) {
	f := newFixture(t)
	r := httptest.NewRequest("GET", "/v1/chats", nil)
	r.Header.Set("Authorization", "Bearer "+f.token(f.id, "wrong"))
	w := httptest.NewRecorder()
	f.c.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("issuer not enforced")
	}
	in := f.input(t, "op", "hello")
	f.require(t, "POST", "/v1/chats/chat/messages", in, 201, false)
	f.require(t, "POST", "/v1/chats/chat/receipts", map[string]int{"delivered": 1, "read": 1}, 200, false)
	f.require(t, "POST", "/v1/chats/chat/receipts", map[string]int{"delivered": 0, "read": 1}, 400, false)
	f.c.DB.Exec(`DELETE FROM events`)
	if _, err := f.c.Events(context.Background(), f.id, "chat", 0, 100); err == nil {
		t.Fatal("expired cursor silently accepted")
	}
	f.require(t, "DELETE", "/management/v1/users/alice/devices/alice-phone", nil, 200, true)
	f.require(t, "GET", "/v1/chats", nil, 401, false)
}
func TestWebsocketTicketsReplayAndRevocation(t *testing.T) {
	f := newFixture(t)
	f.require(t, "POST", "/v1/chats/chat/messages", f.input(t, "ws-op", "offline"), 201, false)
	data := f.require(t, "POST", "/v1/ws-tickets", nil, 201, false)
	var result struct {
		Ticket string `json:"ticket"`
	}
	json.Unmarshal(data, &result)
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/ws?ticket=" + result.Ticket
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err, resp)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	conn.WriteJSON(map[string]any{"type": "subscribe", "chat_id": "chat", "after": 0})
	var event Event
	if err = conn.ReadJSON(&event); err != nil || event.Type != "message.created" {
		t.Fatal("WS offline replay", event, err)
	}
	if second, _, e := websocket.DefaultDialer.Dial(url, nil); e == nil {
		second.Close()
		t.Fatal("ticket reused")
	}
	f.require(t, "DELETE", "/management/v1/users/alice/devices/alice-phone", nil, 200, true)
	if _, _, err = conn.ReadMessage(); err == nil {
		t.Fatal("revoked socket active")
	}
}
func FuzzStrictJSON(f *testing.F) {
	f.Add([]byte(`{"operation_id":"a"}`))
	f.Add([]byte(`{} {}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		w := httptest.NewRecorder()
		var in MessageInput
		Decode(w, r, &in, 4096)
	})
}
