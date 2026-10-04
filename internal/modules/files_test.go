//go:build qg_files

package modules

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func fileFixture(t testing.TB) (*fileStore, core.Identity) {
	t.Helper()
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, e = db.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE chats(id TEXT PRIMARY KEY,mode TEXT); INSERT INTO chats VALUES('chat','basic'); CREATE TABLE messages(id TEXT PRIMARY KEY,chat_id TEXT,seq INTEGER,deleted INTEGER DEFAULT 0); CREATE TABLE members(chat_id TEXT,user_id TEXT,role TEXT,can_send INTEGER,joined_seq INTEGER,active INTEGER); INSERT INTO members VALUES('chat','alice','member',1,0,1);`)
	if e != nil {
		t.Fatal(e)
	}
	cfg := config.Defaults()
	cfg.Storage.Files = t.TempDir()
	engine, e := cryptoenc.New(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	c := &core.Core{DB: db, Config: cfg, Engine: engine, Mux: http.NewServeMux(), Context: context.Background()}
	if e = installFiles(c); e != nil {
		t.Fatal(e)
	}
	return &fileStore{c: c}, core.Identity{UserID: "alice", DeviceID: "phone"}
}

func TestOpaqueE2EEUploadExpiryAndOwnership(t *testing.T) {
	s, id := fileFixture(t)
	if _, e := s.c.DB.Exec(`UPDATE chats SET mode='e2ee'`); e != nil {
		t.Fatal(e)
	}
	ciphertext := []byte("opaque-client-MLS-protected-file-bytes")
	sum := sha256.Sum256(ciphertext)
	digest := hex.EncodeToString(sum[:])
	body, _ := json.Marshal(map[string]any{"operation_id": "opaque", "size": len(ciphertext), "chunks": 1, "sha256": digest})
	created := callFile(s.create, id, body, map[string]string{"chat": "chat"}, "")
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var out struct {
		ID string `json:"id"`
	}
	json.Unmarshal(created.Body.Bytes(), &out)
	values := map[string]string{"upload": out.ID, "index": "0"}
	if w := callFile(s.chunk, core.Identity{UserID: "mallory", DeviceID: "other"}, ciphertext, values, digest); w.Code != 404 {
		t.Fatalf("foreign upload access %d", w.Code)
	}
	if w := callFile(s.chunk, id, ciphertext, values, digest); w.Code != 201 {
		t.Fatalf("opaque chunk %d %s", w.Code, w.Body)
	}
	if w := callFile(s.complete, id, nil, values, ""); w.Code != 200 {
		t.Fatalf("opaque complete %d", w.Code)
	}
	if w := callFile(s.key, id, nil, values, ""); w.Code != 409 {
		t.Fatalf("opaque server key %d", w.Code)
	}
	if _, e := s.c.DB.Exec(`UPDATE uploads SET expires=? WHERE id=?`, time.Now().Unix()-1, out.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.cleanup(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.get(context.Background(), out.ID); e == nil {
		t.Fatal("expired unpublished upload survived")
	}
	if _, e := os.Stat(s.path(out.ID, 0)); !os.IsNotExist(e) {
		t.Fatal("expired chunk survived")
	}
}
func callFile(fn core.Handler, id core.Identity, body []byte, values map[string]string, checksum string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	for k, v := range values {
		r.SetPathValue(k, v)
	}
	if checksum != "" {
		r.Header.Set("X-Chunk-SHA256", checksum)
	}
	w := httptest.NewRecorder()
	fn(w, r, id)
	return w
}
func TestFileUploadResumeIntegrityAndQuota(t *testing.T) {
	s, id := fileFixture(t)
	session := bytes.Repeat([]byte{3}, 32)
	block, _ := aes.NewCipher(session)
	aead, _ := cipher.NewGCM(block)
	nonce := bytes.Repeat([]byte{4}, 12)
	chunk := aead.Seal(append([]byte{}, nonce...), nonce, []byte("private file"), cryptoenc.Binding("chat", "alice", "phone", "op/0"))
	sum := sha256.Sum256(chunk)
	digest := hex.EncodeToString(sum[:])
	// Obtain the receiver public key from the engine rather than exposing storage keys.
	public, err := base64.StdEncoding.DecodeString(s.c.Engine.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	env, err := cryptoenc.SealEnvelope(public, session, cryptoenc.Binding("chat", "alice", "phone", "op"))
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"operation_id": "op", "size": len(chunk), "chunks": 1, "sha256": digest, "envelope": env}
	body, _ := json.Marshal(metadata)
	created := callFile(s.create, id, body, map[string]string{"chat": "chat"}, "")
	if created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	var result struct {
		ID string `json:"id"`
	}
	json.Unmarshal(created.Body.Bytes(), &result)
	values := map[string]string{"upload": result.ID, "index": "0"}
	if w := callFile(s.chunk, id, chunk, values, digest); w.Code != 201 {
		t.Fatalf("chunk %d %s", w.Code, w.Body)
	}
	if w := callFile(s.chunk, id, chunk, values, digest); w.Code != 200 {
		t.Fatalf("repeat chunk %d", w.Code)
	}
	if err = os.Remove(s.path(result.ID, 0)); err != nil {
		t.Fatal(err)
	}
	if w := callFile(s.chunk, id, chunk, values, digest); w.Code != 201 {
		t.Fatalf("resume missing disk chunk %d %s", w.Code, w.Body)
	}
	if w := callFile(s.complete, id, nil, values, ""); w.Code != 200 {
		t.Fatalf("complete %d %s", w.Code, w.Body)
	}
	if w := callFile(s.download, id, nil, values, ""); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), chunk) {
		t.Fatalf("download %d", w.Code)
	}
	stored, err := os.ReadFile(s.path(result.ID, 0))
	if err != nil || bytes.Equal(stored, chunk) || bytes.Contains(stored, []byte("private file")) {
		t.Fatal("disk chunk not encrypted")
	}
	if w := callFile(s.create, id, body, map[string]string{"chat": "chat"}, ""); w.Code != 200 {
		t.Fatalf("idempotent create %d", w.Code)
	}
	metadata["sha256"] = hex.EncodeToString(bytes.Repeat([]byte{8}, 32))
	body, _ = json.Marshal(metadata)
	if w := callFile(s.create, id, body, map[string]string{"chat": "chat"}, ""); w.Code != 409 {
		t.Fatalf("metadata conflict %d", w.Code)
	}
	s.c.Config.Policy.MaxStorageBytes = int64(len(chunk))
	metadata["operation_id"] = "another"
	env, _ = cryptoenc.SealEnvelope(public, session, cryptoenc.Binding("chat", "alice", "phone", "another"))
	metadata["envelope"] = env
	body, _ = json.Marshal(metadata)
	if w := callFile(s.create, id, body, map[string]string{"chat": "chat"}, ""); w.Code != 507 {
		t.Fatalf("quota %d %s", w.Code, w.Body)
	}
	if err := s.c.Prepare[0](context.Background(), id, "other", &core.MessageInput{Attachments: []string{result.ID}}); err == nil {
		t.Fatal("cross-chat publication permitted")
	}
	if _, err = s.c.DB.Exec(`INSERT INTO members VALUES('chat','bob','member',1,0,1); INSERT INTO messages VALUES('published','chat',1,0)`); err != nil {
		t.Fatal(err)
	}
	tx, e := s.c.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	for _, hook := range s.c.InTransaction {
		if e = hook(context.Background(), tx, id, "chat", core.Message{ID: "published", ChatID: "chat", Seq: 1, Attachments: []string{result.ID}}); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	bob := core.Identity{UserID: "bob", DeviceID: "bob-phone"}
	if w := callFile(s.status, bob, nil, values, ""); w.Code != 200 {
		t.Fatalf("peer file metadata %d %s", w.Code, w.Body)
	}
	if _, err = s.c.DB.Exec(`UPDATE members SET joined_seq=2 WHERE user_id='bob'`); err != nil {
		t.Fatal(err)
	}
	if w := callFile(s.status, bob, nil, values, ""); w.Code != 404 {
		t.Fatalf("history-restricted file metadata %d", w.Code)
	}
}
