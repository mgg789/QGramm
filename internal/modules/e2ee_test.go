//go:build qg_e2ee

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"github.com/thomas-vilte/mls-go/framing"
)

func mlsFixture(t *testing.T) (*core.Core, *cryptoenc.MLSParticipant, *cryptoenc.MLSParticipant) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE users(id TEXT PRIMARY KEY,disabled INTEGER DEFAULT 0); CREATE TABLE devices(id TEXT PRIMARY KEY,user_id TEXT,signing_key TEXT,revoked INTEGER DEFAULT 0); CREATE TABLE chats(id TEXT PRIMARY KEY,mode TEXT,epoch INTEGER DEFAULT 0,pending INTEGER DEFAULT 1,seq INTEGER DEFAULT 0); CREATE TABLE members(chat_id TEXT,user_id TEXT,role TEXT,can_send INTEGER,joined_seq INTEGER,active INTEGER); CREATE TABLE events(chat_id TEXT,seq INTEGER,kind TEXT,message_id TEXT,data BLOB,created_at INTEGER); INSERT INTO users(id) VALUES('alice'),('bob'); INSERT INTO chats(id,mode) VALUES('chat','e2ee'); INSERT INTO members VALUES('chat','alice','member',1,0,1),('chat','bob','member',1,0,1);`)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := cryptoenc.New(bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	c := &core.Core{DB: db, Engine: engine, Config: config.Defaults(), Mux: http.NewServeMux(), Context: context.Background()}
	if err = installE2EE(c); err != nil {
		t.Fatal(err)
	}
	alice, err := cryptoenc.NewMLS([]byte("alice-phone"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := cryptoenc.NewMLS([]byte("bob-phone"))
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []struct {
		ID, User string
		P        *cryptoenc.MLSParticipant
	}{{"alice-phone", "alice", alice}, {"bob-phone", "bob", bob}} {
		if _, err = db.Exec(`INSERT INTO devices(id,user_id,signing_key) VALUES(?,?,?)`, device.ID, device.User, base64.StdEncoding.EncodeToString(device.P.SigningPublicKey())); err != nil {
			t.Fatal(err)
		}
	}
	return c, alice, bob
}
func mlsRequest(body any, values map[string]string) (*httptest.ResponseRecorder, *http.Request) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
	for k, v := range values {
		req.SetPathValue(k, v)
	}
	return httptest.NewRecorder(), req
}
func mlsSigning(t *testing.T, c *core.Core, p *cryptoenc.MLSParticipant) ed25519.PrivateKey {
	t.Helper()
	snapshot, err := p.Snapshot(c.Engine, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Engine.Open(snapshot, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Signing []byte }
	if err = json.Unmarshal(plain, &out); err != nil {
		t.Fatal(err)
	}
	return ed25519.PrivateKey(out.Signing)
}
func TestMLSDeviceBoundKeyPackageClaimOnce(t *testing.T) {
	c, alice, bob := mlsFixture(t)
	id := core.Identity{UserID: "bob", DeviceID: "bob-phone"}
	w, r := mlsRequest(map[string]string{"key_package": base64.StdEncoding.EncodeToString(alice.KeyPackage())}, nil)
	uploadMLSKeyPackage(c, w, r, id)
	if w.Code != 400 {
		t.Fatalf("foreign identity accepted: %d", w.Code)
	}
	impersonator, _ := cryptoenc.NewMLS([]byte("bob-phone"))
	w, r = mlsRequest(map[string]string{"key_package": base64.StdEncoding.EncodeToString(impersonator.KeyPackage())}, nil)
	uploadMLSKeyPackage(c, w, r, id)
	if w.Code != 403 {
		t.Fatalf("wrong signing key accepted: %d", w.Code)
	}
	w, r = mlsRequest(map[string]string{"key_package": base64.StdEncoding.EncodeToString(bob.KeyPackage())}, nil)
	uploadMLSKeyPackage(c, w, r, id)
	if w.Code != 201 {
		t.Fatalf("upload %d: %s", w.Code, w.Body)
	}
	for i, want := range []int{200, 409} {
		w, r = mlsRequest(nil, map[string]string{"chat": "chat", "device": "bob-phone"})
		claimMLSKeyPackage(c, w, r, core.Identity{UserID: "alice", DeviceID: "alice-phone"})
		if w.Code != want {
			t.Fatalf("claim %d = %d: %s", i, w.Code, w.Body)
		}
	}
	var ciphertext []byte
	if err := c.DB.QueryRow(`SELECT payload FROM mls_keypackages`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, bob.KeyPackage()) {
		t.Fatal("KeyPackage stored unencrypted")
	}
}
func TestMLSRelayEpochBarrierSignedRosterAndWire(t *testing.T) {
	c, alice, bob := mlsFixture(t)
	if _, err := alice.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	_, welcome, err := alice.Invite(bob.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bob.Join(welcome); err != nil {
		t.Fatal(err)
	}
	roster := []MLSRelayMember{{"alice-phone", 0}, {"bob-phone", 1}}
	ctx := context.Background()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = InitMLSRelayTx(ctx, c, tx, "chat", alice.GroupID(), alice.GroupContext(), alice.Epoch(), roster); err != nil {
		t.Fatal(err)
	}
	if err = StoreMLSWelcomeTx(ctx, c, tx, "chat", "bob-phone", alice.Epoch(), welcome); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	id := core.Identity{UserID: "alice", DeviceID: "alice-phone"}
	aad := cryptoenc.Binding("chat", id.UserID, id.DeviceID, "op")
	wire, err := alice.Encrypt([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	input := core.MessageInput{MLS: base64.StdEncoding.EncodeToString(wire), Epoch: int64(alice.Epoch()), OperationID: "op"}
	if err = c.Prepare[0](ctx, id, "chat", &input); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(input.Payload, wire) {
		t.Fatal("relay changed MLS wire")
	}
	input.MLS = base64.StdEncoding.EncodeToString([]byte("plaintext"))
	if err = c.Prepare[0](ctx, id, "chat", &input); err == nil {
		t.Fatal("accepted arbitrary plaintext MLS")
	}
	other, _ := cryptoenc.NewMLS([]byte("other"))
	other.CreateGroup()
	wrong, _ := other.Encrypt([]byte("x"), aad)
	input.MLS = base64.StdEncoding.EncodeToString(wrong)
	if err = c.Prepare[0](ctx, id, "chat", &input); err == nil {
		t.Fatal("accepted other group")
	}
	// A signed commit advances the cryptographic group locally, while the
	// relay remains at the old epoch with an explicit pending barrier.
	third, _ := cryptoenc.NewMLS([]byte("alice-tablet"))
	_, err = c.DB.Exec(`INSERT INTO devices(id,user_id,signing_key) VALUES(?,?,?)`, "alice-tablet", "alice", base64.StdEncoding.EncodeToString(third.SigningPublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	previous := alice.Epoch()
	commit, _, err := alice.Invite(third.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(commit)
	parsed, _ := framing.UnmarshalMLSMessage(commit)
	public, _ := parsed.AsPublic()
	offset := bytes.Index(tampered, public.Auth.Signature.AsSlice())
	if offset < 0 {
		t.Fatal("signature not found")
	}
	tampered[offset] ^= 1
	w, r := mlsRequest(map[string]string{"commit": base64.StdEncoding.EncodeToString(tampered)}, map[string]string{"chat": "chat"})
	submitMLSCommit(c, w, r, id)
	if w.Code < 400 {
		t.Fatal("accepted modified commit")
	}
	w, r = mlsRequest(map[string]string{"commit": base64.StdEncoding.EncodeToString(commit)}, map[string]string{"chat": "chat"})
	submitMLSCommit(c, w, r, id)
	if w.Code != 202 {
		t.Fatalf("commit %d: %s", w.Code, w.Body)
	}
	var out struct {
		TransitionID string `json:"transition_id"`
		CommitHash   string `json:"commit_hash"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	input.MLS = base64.StdEncoding.EncodeToString(wire)
	if err = c.Prepare[0](ctx, id, "chat", &input); err == nil {
		t.Fatal("pending barrier bypass")
	}
	next := append(roster, MLSRelayMember{"alice-tablet", 2})
	proof := MLSRosterProof("chat", alice.GroupID(), int64(previous), alice.Epoch(), out.CommitHash, alice.GroupContext(), next)
	sig := ed25519.Sign(mlsSigning(t, c, alice), proof)
	body := map[string]any{"transition_id": out.TransitionID, "group_id": base64.StdEncoding.EncodeToString(alice.GroupID()), "group_context": base64.StdEncoding.EncodeToString(alice.GroupContext()), "previous_epoch": previous, "epoch": alice.Epoch(), "signer_device": "alice-phone", "signature": base64.StdEncoding.EncodeToString(sig), "roster": next}
	bad := append([]MLSRelayMember(nil), next...)
	bad[1].DeviceID = "not-registered"
	body["roster"] = bad
	w, r = mlsRequest(body, map[string]string{"chat": "chat"})
	confirmMLSEpoch(c, w, r)
	if w.Code != 409 {
		t.Fatalf("bad roster %d", w.Code)
	}
	body["roster"] = next
	w, r = mlsRequest(body, map[string]string{"chat": "chat"})
	confirmMLSEpoch(c, w, r)
	if w.Code != 200 {
		t.Fatalf("confirm %d: %s", w.Code, w.Body)
	}
	w, r = mlsRequest(body, map[string]string{"chat": "chat"})
	confirmMLSEpoch(c, w, r)
	if w.Code != 409 {
		t.Fatal("accepted stale epoch confirmation")
	}
	var storedEpoch int64
	var pending bool
	c.DB.QueryRow(`SELECT epoch,pending FROM chats WHERE id='chat'`).Scan(&storedEpoch, &pending)
	if pending || storedEpoch != int64(alice.Epoch()) {
		t.Fatal("barrier not atomically released")
	}
	// Targeted Welcome delivery cannot expose another device's inbox.
	w, r = mlsRequest(nil, map[string]string{"chat": "chat"})
	getMLSInbox(c, w, r, id)
	var inbox struct {
		Welcomes []any `json:"welcomes"`
	}
	json.Unmarshal(w.Body.Bytes(), &inbox)
	if len(inbox.Welcomes) != 0 {
		t.Fatal("foreign Welcome exposed")
	}
}
