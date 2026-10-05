//go:build qg_groups && qg_delete && qg_edit && qg_reactions && qg_reply && qg_forward

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type moduleHarness struct {
	c    *core.Core
	sign ed25519.PrivateKey
	id   core.Identity
	cfg  config.Config
}

func newModuleHarness(t *testing.T, deleteMode string) *moduleHarness {
	t.Helper()
	cfg, err := config.Load("../../configs/full.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Features = config.Features{}
	for _, name := range core.Registered() {
		switch name {
		case "groups":
			cfg.Features.Groups = true
		case "files":
			cfg.Features.Files = true
		case "e2ee":
			cfg.Features.E2EE = true
		case "calls":
			cfg.Features.Calls = true
		case "delete":
			cfg.Features.Delete = true
		case "edit":
			cfg.Features.Edit = true
		case "reply":
			cfg.Features.Reply = true
		case "forward":
			cfg.Features.Forward = true
		case "reactions":
			cfg.Features.Reactions = true
		case "openai":
			cfg.Features.OpenAI = true
		case "anthropic":
			cfg.Features.Anthropic = true
		case "ai_storage":
			cfg.Features.AIStorage = true
		case "ai_endpoint":
			cfg.Features.AIEndpoint = true
		case "ai_policy":
			cfg.Features.AIPolicy = true
		case "ai_streaming":
			cfg.Features.AIStreaming = true
		case "mcp":
			cfg.Features.MCP = true
		case "http_tools":
			cfg.Features.HTTPTools = true
		}
	}
	cfg.AI.Tools = nil
	cfg.Policy.DeleteMode = deleteMode
	cfg.Storage.Path = filepath.Join(t.TempDir(), "core.db")
	cfg.Storage.Files = t.TempDir()
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(cfg.Security.TokenPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.AIPolicy.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.AIStorage.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(public))
	t.Setenv(cfg.Security.ManagementSecretEnv, strings.Repeat("m", 32))
	for _, env := range []string{cfg.Security.MasterKeyEnv, cfg.Security.HPKEKeyEnv} {
		b := make([]byte, 32)
		rand.Read(b)
		t.Setenv(env, base64.StdEncoding.EncodeToString(b))
	}
	for _, env := range []string{cfg.Calls.TURNSecretEnv, cfg.AI.OpenAIKeyEnv, cfg.AI.AnthropicKeyEnv} {
		t.Setenv(env, "non-secret-test-fixture")
	}
	c, err := core.Open(cfg, cfg.Features.Enabled())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	h := &moduleHarness{c, private, core.Identity{UserID: "alice", DeviceID: "phone"}, cfg}
	for _, user := range []string{"alice", "bob", "charlie"} {
		c.DB.Exec(`INSERT INTO users(id) VALUES(?)`, user)
	}
	c.DB.Exec(`INSERT INTO devices(id,user_id,public_key) VALUES('phone','alice',?),('bob-phone','bob',?)`, c.Engine.PublicKey(), c.Engine.PublicKey())
	h.request(t, "POST", "/management/v1/chats/groups", map[string]any{"id": "chat", "members": []string{"alice", "bob"}}, 201, true)
	return h
}
func (h *moduleHarness) request(t *testing.T, method, path string, body any, status int, manage bool) []byte {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if manage {
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("m", 32))
	} else {
		now := time.Now()
		token, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"sub": h.id.UserID, "device_id": h.id.DeviceID, "iss": h.cfg.Security.Issuer, "aud": h.cfg.Security.Audience, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}).SignedString(h.sign)
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.c.Handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d wanted %d %s", method, path, w.Code, status, w.Body)
	}
	return w.Body.Bytes()
}
func (h *moduleHarness) input(t *testing.T, op string) core.MessageInput {
	t.Helper()
	pub, _ := base64.StdEncoding.DecodeString(h.c.Engine.PublicKey())
	env, err := cryptoenc.SealEnvelope(pub, []byte("protected payload"), cryptoenc.Binding("chat", h.id.UserID, h.id.DeviceID, op))
	if err != nil {
		t.Fatal(err)
	}
	return core.MessageInput{OperationID: op, Envelope: &env}
}
func TestMessageMutationsPermissionsAndConvergence(t *testing.T) {
	h := newModuleHarness(t, "global")
	m, err := h.c.Send(context.Background(), h.id, "chat", h.input(t, "send"))
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/chats/chat/messages/" + m.ID
	h.id = core.Identity{UserID: "bob", DeviceID: "bob-phone"}
	h.request(t, "PATCH", path, map[string]any{"operation_id": "foreign-edit", "expected_revision": 1, "message": h.input(t, "foreign-edit")}, 403, false)
	h.id = core.Identity{UserID: "alice", DeviceID: "phone"}
	edit := map[string]any{"operation_id": "edit", "expected_revision": 1, "message": h.input(t, "edit")}
	h.request(t, "PATCH", path, edit, 200, false)
	h.request(t, "PATCH", path, edit, 200, false)
	h.request(t, "PATCH", path, map[string]any{"operation_id": "stale", "expected_revision": 1, "message": h.input(t, "stale")}, 409, false)
	h.request(t, "POST", path+"/reactions", map[string]any{"operation_id": "reaction", "type": 0}, 200, false)
	h.request(t, "POST", path+"/reactions", map[string]any{"operation_id": "bad-reaction", "type": 100}, 400, false)
	h.request(t, "DELETE", path, map[string]string{"operation_id": "delete"}, 200, false)
	h.request(t, "PATCH", path, map[string]any{"operation_id": "after-delete", "expected_revision": 3, "message": h.input(t, "after-delete")}, 409, false)
	view, err := h.c.ViewMessage(context.Background(), h.id, m.ID)
	if err != nil || !view.Deleted || view.Envelope != nil {
		t.Fatal("deleted payload projection", err)
	}
	events, err := h.c.Events(context.Background(), h.id, "chat", 0, 100)
	if err != nil || len(events) != 4 {
		t.Fatal("operation replay events", len(events), err)
	}
	for _, event := range events {
		if event.MessageID == m.ID {
			if msg, ok := event.Data.(core.Message); !ok || !msg.Deleted {
				t.Fatal("old event leaked deleted payload")
			}
		}
	}
	bad := h.input(t, "reply")
	bad.ReplyTo = m.ID
	if _, err = h.c.Send(context.Background(), h.id, "chat", bad); err == nil {
		t.Fatal("reply to deleted accepted")
	}
}
func TestAuthorOnlyHidingAndJoinHistory(t *testing.T) {
	h := newModuleHarness(t, "author_only")
	m, err := h.c.Send(context.Background(), h.id, "chat", h.input(t, "send"))
	if err != nil {
		t.Fatal(err)
	}
	h.request(t, "DELETE", "/v1/chats/chat/messages/"+m.ID, map[string]string{"operation_id": "hide"}, 200, false)
	alice, err := h.c.ViewMessage(context.Background(), h.id, m.ID)
	if err != nil || !alice.Deleted {
		t.Fatal("author hide failed", err)
	}
	bob, err := h.c.ViewMessage(context.Background(), core.Identity{UserID: "bob", DeviceID: "bob-phone"}, m.ID)
	if err != nil || bob.Deleted || bob.Envelope == nil {
		t.Fatal("hide affected peer", err)
	}
	h.request(t, "PUT", "/management/v1/chats/chat/members/charlie", map[string]any{"role": "member", "can_send": true, "active": true}, 200, true)
	if _, err = h.c.ViewMessage(context.Background(), core.Identity{UserID: "charlie", DeviceID: "new"}, m.ID); err == nil {
		t.Fatal("new member got prior history")
	}
	h.request(t, "PUT", "/management/v1/chats/chat/members/alice", map[string]any{"role": "owner", "can_send": false, "active": true}, 200, true)
	if _, err = h.c.Send(context.Background(), h.id, "chat", h.input(t, "denied")); err == nil {
		t.Fatal("can_send false ignored")
	}
}
