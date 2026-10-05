//go:build qg_ai_endpoint && qg_e2ee

package microsafer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func testConfig(t *testing.T, db string) Config {
	t.Helper()
	return Config{Endpoint: EndpointConfig{Name: "endpoint", User: "alice", Device: "alice-device", ServerURL: "http://127.0.0.1:1", TokenEnv: "TEST_MICRO_TOKEN", MasterKeyEnv: "TEST_MICRO_MASTER", HPKEKeyEnv: "TEST_MICRO_HPKE", DBPath: db, GrantPublicKeyEnv: "TEST_MICRO_GRANT", Issuer: "issuer", Audience: "audience"}}
}

func TestConfigSeparatesStorageAndRPCGrants(t *testing.T) {
	c := testConfig(t, filepath.Join(t.TempDir(), "state.db"))
	c.Storage = StorageConfig{Enabled: true, GrantPublicKeyEnv: "TEST_STORAGE_GRANT", Issuer: "issuer", Audience: c.Endpoint.Audience}
	c.setDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("accepted interchangeable storage and RPC audiences")
	}
	c.Storage.Audience = "storage-audience"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTransportEnforcesRequestDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"messages":[],"cursor":"1"}`))
	}))
	defer server.Close()
	tx := &HTTPTransport{BaseURL: server.URL, Client: &http.Client{Timeout: 10 * time.Millisecond}, MaxBody: 1 << 20}
	if _, err := tx.Poll(context.Background(), "chat", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("poll error = %v, want context deadline exceeded", err)
	}
}

func TestMLSStoreSnapshotAndRuntimeIdempotency(t *testing.T) {
	db := filepath.Join(t.TempDir(), "state.db")
	c := testConfig(t, db)
	key := bytes.Repeat([]byte{3}, 32)
	hpke := bytes.Repeat([]byte{4}, 32)
	s, err := OpenStoreWithKeys(c, key, hpke)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := New("alice", "alice-device")
	if err != nil {
		t.Fatal(err)
	}
	a.attach(s, "chat")
	b, err := cryptoenc.NewMLS([]byte("bob-device"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.MLS.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	_, welcome, err := a.MLS.Invite(b.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Join(welcome); err != nil {
		t.Fatal(err)
	}
	if err = a.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(c, s, a, "chat")
	if got, want := r.transport.Client.Timeout, 120*time.Second; got != want {
		t.Fatalf("runtime HTTP timeout = %s, want %s", got, want)
	}
	if err = r.RegisterHandler("echo", false, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) { return body, nil }); err != nil {
		t.Fatal(err)
	}
	req := RPCRequest{Version: 1, RequestID: "request-1", ClientID: "client-1", Chat: "chat", SourcePeer: "bob-device", Action: "invoke", Name: "echo", Body: json.RawMessage(`{"value":1}`)}
	out, err := r.ProcessRPC(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != "result" {
		t.Fatalf("unexpected response: %#v", out)
	}
	again, err := r.ProcessRPC(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].RequestID != "request-1" {
		t.Fatalf("dedup response: %#v", again)
	}
	conflict := req
	conflict.Body = json.RawMessage(`{"value":2}`)
	if _, err = r.ProcessRPC(context.Background(), conflict); err == nil {
		t.Fatal("accepted idempotency hash conflict")
	}
	pending, err := s.PendingOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || len(pending[0].Wire) == 0 || len(pending[1].Wire) == 0 {
		t.Fatalf("outbox: %#v", pending)
	}
	for _, item := range pending {
		plain, decErr := b.Decrypt(item.Wire, cryptoenc.Binding("chat", "alice", "alice-device", item.OperationID))
		if decErr != nil {
			t.Fatalf("response AAD %s: %v", item.OperationID, decErr)
		}
		var response RPCResponse
		if err = json.Unmarshal(plain, &response); err != nil || response.RequestID != "request-1" {
			t.Fatalf("response wire: %s %v", plain, err)
		}
	}
	// The persisted snapshot is sufficient to restore the same MLS epoch.
	var blob []byte
	if err = s.GetState(context.Background(), "mls/chat", &blob); err != nil {
		t.Fatal(err)
	}
	if _, err = cryptoenc.RestoreMLS(s.Engine(), blob, a.snapshotAAD()); err != nil {
		t.Fatal(err)
	}
	incoming := RPCRequest{Version: 1, RequestID: "incoming-1", ClientID: "incoming-client", Chat: "chat", SourcePeer: "bob-device", Action: "invoke", Name: "echo", Body: json.RawMessage(`{"value":3}`)}
	incomingBody, _ := json.Marshal(incoming)
	incomingWire, err := b.Encrypt(incomingBody, cryptoenc.Binding("chat", "bob", "bob-device", "incoming-op"))
	if err != nil {
		t.Fatal(err)
	}
	msg := RelayMessage{OperationID: "incoming-op", SenderUser: "bob", SenderDevice: "bob-device", Epoch: a.MLS.Epoch(), Wire: base64.StdEncoding.EncodeToString(incomingWire)}
	if err = r.receive(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err = r.receive(context.Background(), msg); err != nil {
		t.Fatal("durable inbox replay: ", err)
	}
	inbox, err := s.GetInbox(context.Background(), "incoming-op")
	if err != nil || inbox.Status != "done" {
		t.Fatalf("inbox state: %#v %v", inbox, err)
	}
}

func TestConfigDoesNotReadSecretValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qgramm.toml")
	text := `[endpoint]
name="endpoint"
user="alice"
device="alice-device"
server_url="http://127.0.0.1:1"
token_env="TEST_MICRO_TOKEN"
master_key_env="TEST_MICRO_MASTER"
hpke_key_env="TEST_MICRO_HPKE"
db_path="state.db"
grant_public_key_env="TEST_MICRO_GRANT"
issuer="issuer"
audience="audience"
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Endpoint.MasterKeyEnv != "TEST_MICRO_MASTER" {
		t.Fatal("config loader changed env reference")
	}
	if _, err := base64.StdEncoding.DecodeString("not-a-secret"); err == nil {
		t.Fatal("test fixture unexpectedly decoded")
	}
	if err := os.WriteFile(path, append([]byte(text), []byte("unknown_field = true\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("accepted unknown TOML field")
	}
}

func TestCanonicalJSONRejectsNestedDuplicates(t *testing.T) {
	if _, err := CanonicalJSON([]byte(`{"a":{"x":1,"x":2}}`)); err == nil {
		t.Fatal("accepted duplicate nested JSON key")
	}
	if got, err := CanonicalJSON([]byte(` { "b": 2, "a": 1 } `)); err != nil || string(got) != "{\"a\":1,\"b\":2}" {
		t.Fatalf("canonical JSON: %s %v", got, err)
	}
}

func TestJoinWelcomeChecksOfflineRosterPins(t *testing.T) {
	key, hpke := bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32)
	alice, _ := cryptoenc.NewMLS([]byte("alice-device"))
	bob, _ := New("bob", "bob-device")
	gid, _ := alice.CreateGroup()
	_, welcome, err := alice.Invite(bob.MLS.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	c := testConfig(t, filepath.Join(t.TempDir(), "bob.db"))
	c.Endpoint.User, c.Endpoint.Device = "bob", "bob-device"
	c.Peers = map[string]PeerConfig{"human": {User: "alice", Device: "alice-device", SigningKey: base64.StdEncoding.EncodeToString(alice.SigningPublicKey()), GroupID: base64.StdEncoding.EncodeToString(gid)}}
	c.Chats = map[string]ChatConfig{"chat": {Peer: "human"}}
	// JoinWelcome obtains keys from the environment through OpenStore.
	os.Setenv(c.Endpoint.MasterKeyEnv, base64.StdEncoding.EncodeToString(key))
	os.Setenv(c.Endpoint.HPKEKeyEnv, base64.StdEncoding.EncodeToString(hpke))
	defer os.Unsetenv(c.Endpoint.MasterKeyEnv)
	defer os.Unsetenv(c.Endpoint.HPKEKeyEnv)
	s, err := OpenStoreWithKeys(c, key, hpke)
	if err != nil {
		t.Fatal(err)
	}
	bob.attach(s, "chat")
	if err = bob.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = JoinWelcome(context.Background(), c, "chat", welcome); err != nil {
		t.Fatal(err)
	}
}
