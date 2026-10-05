//go:build qg_ai_endpoint && qg_e2ee && qg_ai_storage

package microsafer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/aivault"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestStorageHandlerSignedGrantAllowlistAndSingleUse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	master := make([]byte, 32)
	hpke := make([]byte, 32)
	_, _ = rand.Read(master)
	_, _ = rand.Read(hpke)
	c := testConfig(t, filepath.Join(t.TempDir(), "endpoint.db"))
	c.Storage = StorageConfig{Enabled: true, DBPath: filepath.Join(t.TempDir(), "vault.db"), MasterKeyEnv: "MICRO_VAULT_MASTER", HPKEKeyEnv: "MICRO_VAULT_HPKE", GrantPublicKeyEnv: "MICRO_VAULT_GRANT", Issuer: "vault-issuer", Audience: "vault-audience", Resources: map[string]StorageResourceConfig{"r": {Scope: "user", Owner: "bob"}}}
	t.Setenv(c.Storage.MasterKeyEnv, base64.StdEncoding.EncodeToString(master))
	t.Setenv(c.Storage.HPKEKeyEnv, base64.StdEncoding.EncodeToString(hpke))
	t.Setenv(c.Storage.GrantPublicKeyEnv, base64.StdEncoding.EncodeToString(pub))
	h, err := OpenStorage(c)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err = h.Vault.CreateResource(context.Background(), aivault.ResourceSpec{ID: "r", Scope: aivault.ScopeUser, Owner: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err = h.Vault.PutDocument(context.Background(), "r", aivault.DocumentInput{ID: "d", Text: "secret"}); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"document_id":"d"}`)
	hash, _ := RequestHash(body)
	signer, _ := aivault.NewGrantSigner(priv, "vault-issuer", "vault-audience", 5*time.Minute)
	token, err := signer.Sign(aivault.Claims{Nonce: "nonce-1", RequestID: "storage-request", RequestHash: base64.RawURLEncoding.EncodeToString(hash), ChatID: "chat", SourceUser: "bob", AIUser: "alice", Resource: "r", Action: aivault.ActionRead, Destination: "local"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(StorageRequest{Action: "read", Resource: "r", Destination: "local", Grant: token, Body: body})
	req := RPCRequest{Version: 1, RequestID: "storage-request", ClientID: "storage-client", Chat: "chat", SourcePeer: "bob", Action: "invoke", Name: "storage", Body: raw}
	out, err := h.HandleRPC(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("empty storage response")
	}
	if _, err = h.HandleRPC(context.Background(), req); err == nil {
		t.Fatal("reused storage grant")
	}
	wrong, err := signer.Sign(aivault.Claims{Nonce: "nonce-2", RequestID: "storage-request-2", RequestHash: base64.RawURLEncoding.EncodeToString(hash), ChatID: "chat", SourceUser: "mallory", AIUser: "alice", Resource: "r", Action: aivault.ActionRead, Destination: "local"})
	if err != nil {
		t.Fatal(err)
	}
	wrongBody, _ := json.Marshal(StorageRequest{Action: "read", Resource: "r", Destination: "local", Grant: wrong, Body: body})
	wrongReq := req
	wrongReq.RequestID = "storage-request-2"
	wrongReq.ClientID = "storage-client-2"
	wrongReq.SourcePeer = "mallory"
	wrongReq.Body = wrongBody
	if _, err = h.HandleRPC(context.Background(), wrongReq); err == nil {
		t.Fatal("wrong peer accessed user-scoped resource")
	}
	// A rejected authorization is an application denial, not a runtime
	// freeze: the next independently granted request must still execute.
	valid2, err := signer.Sign(aivault.Claims{Nonce: "nonce-3", RequestID: "storage-request-3", RequestHash: base64.RawURLEncoding.EncodeToString(hash), ChatID: "chat", SourceUser: "bob", AIUser: "alice", Resource: "r", Action: aivault.ActionRead, Destination: "local"})
	if err != nil {
		t.Fatal(err)
	}
	validBody2, _ := json.Marshal(StorageRequest{Action: "read", Resource: "r", Destination: "local", Grant: valid2, Body: body})
	validReq2 := req
	validReq2.RequestID = "storage-request-3"
	validReq2.ClientID = "storage-client-3"
	validReq2.Body = validBody2
	if _, err = h.HandleRPC(context.Background(), validReq2); err != nil {
		t.Fatalf("valid request after denial: %v", err)
	}
	// Retained grant records are bounded without automatic deletion, which
	// preserves replay safety across restarts.
	h.MaxGrantRecords = 2
	valid3, err := signer.Sign(aivault.Claims{Nonce: "nonce-4", RequestID: "storage-request-4", RequestHash: base64.RawURLEncoding.EncodeToString(hash), ChatID: "chat", SourceUser: "bob", AIUser: "alice", Resource: "r", Action: aivault.ActionRead, Destination: "local"})
	if err != nil {
		t.Fatal(err)
	}
	validBody3, _ := json.Marshal(StorageRequest{Action: "read", Resource: "r", Destination: "local", Grant: valid3, Body: body})
	validReq3 := validReq2
	validReq3.RequestID = "storage-request-4"
	validReq3.ClientID = "storage-client-4"
	validReq3.Body = validBody3
	if _, err = h.HandleRPC(context.Background(), validReq3); err == nil {
		t.Fatal("grant retention cap accepted a new grant")
	}
	// Exercise the actual encrypted inbox/runtime boundary, rather than only
	// calling the storage handler: a denied grant must not block later work.
	h.MaxGrantRecords = 32
	h.Peers = map[string]PeerConfig{"chat": {User: "bob", Device: "bob-device"}}
	s, err := OpenStoreWithKeys(c, master, hpke)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := New("alice", "alice-device")
	if err != nil {
		t.Fatal(err)
	}
	p.attach(s, "chat")
	b, err := cryptoenc.NewMLS([]byte("bob-device"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.MLS.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	_, welcome, err := p.MLS.Invite(b.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Join(welcome); err != nil {
		t.Fatal(err)
	}
	r := NewRuntime(c, s, p, "chat")
	if err = AttachStorageHandler(r, h); err != nil {
		t.Fatal(err)
	}
	for _, allowed := range []bool{false, true} {
		id, token := "wire-denied", "invalid"
		if allowed {
			id = "wire-allowed"
			token, err = signer.Sign(aivault.Claims{Nonce: "wire-nonce", RequestID: id, RequestHash: base64.RawURLEncoding.EncodeToString(hash), ChatID: "chat", SourceUser: "bob", AIUser: "alice", Resource: "r", Action: aivault.ActionRead, Destination: "local"})
			if err != nil {
				t.Fatal(err)
			}
		}
		storageBody, _ := json.Marshal(StorageRequest{Action: "read", Resource: "r", Destination: "local", Grant: token, Body: body})
		request := RPCRequest{Version: 1, RequestID: id, ClientID: id, Chat: "chat", SourcePeer: "bob-device", Action: "invoke", Name: "storage", Body: storageBody}
		plain, _ := json.Marshal(request)
		wire, err := b.Encrypt(plain, cryptoenc.Binding("chat", "bob", "bob-device", id))
		if err != nil {
			t.Fatal(err)
		}
		if err = r.receive(context.Background(), RelayMessage{OperationID: id, SenderUser: "bob", SenderDevice: "bob-device", Epoch: b.Epoch(), Wire: base64.StdEncoding.EncodeToString(wire)}); err != nil || r.Frozen() {
			t.Fatalf("storage receive allowed=%v: %v, frozen=%v", allowed, err, r.Frozen())
		}
	}
	frames, err := s.PendingOutbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	gotResult := false
	for _, frame := range frames {
		plain, err := b.Decrypt(frame.Wire, cryptoenc.Binding("chat", "alice", "alice-device", frame.OperationID))
		if err != nil {
			t.Fatal(err)
		}
		var response RPCResponse
		if err = json.Unmarshal(plain, &response); err != nil {
			t.Fatal(err)
		}
		if response.RequestID == "wire-allowed" && response.Kind == "result" {
			gotResult = true
		}
	}
	if !gotResult {
		t.Fatal("no decryptable storage result after denied request")
	}
}
