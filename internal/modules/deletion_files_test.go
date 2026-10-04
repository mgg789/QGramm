//go:build qg_files && qg_groups && qg_delete && qg_edit && qg_reactions && qg_reply && qg_forward

package modules

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/mgg789/QGramm/internal/core"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"os"
	"testing"
)

func TestGlobalDeleteRevokesFileAndReclaimsQuota(t *testing.T) {
	h := newModuleHarness(t, "global")
	s := &fileStore{c: h.c}
	key := bytes.Repeat([]byte{7}, 32)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := bytes.Repeat([]byte{6}, 12)
	chunk := aead.Seal(append([]byte{}, nonce...), nonce, []byte("attachment"), cryptoenc.Binding("chat", "alice", "phone", "file/0"))
	hash := sha256.Sum256(chunk)
	checksum := hex.EncodeToString(hash[:])
	pub, _ := base64.StdEncoding.DecodeString(h.c.Engine.PublicKey())
	env, _ := cryptoenc.SealEnvelope(pub, key, cryptoenc.Binding("chat", "alice", "phone", "file"))
	body, _ := json.Marshal(map[string]any{"operation_id": "file", "size": len(chunk), "chunks": 1, "sha256": checksum, "envelope": env})
	w := callFile(s.create, h.id, body, map[string]string{"chat": "chat"}, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	values := map[string]string{"upload": created.ID, "index": "0"}
	if w = callFile(s.chunk, h.id, chunk, values, checksum); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w = callFile(s.complete, h.id, nil, values, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	in := h.input(t, "publish")
	in.Attachments = []string{created.ID}
	m, err := h.c.Send(context.Background(), h.id, "chat", in)
	if err != nil {
		t.Fatal(err)
	}
	bob := core.Identity{UserID: "bob", DeviceID: "bob-phone"}
	if w = callFile(s.download, bob, nil, values, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	h.request(t, "GET", "/v1/chats/wrong/messages/"+m.ID+"/reactions", nil, 404, false)
	h.request(t, "DELETE", "/v1/chats/chat/messages/"+m.ID, map[string]string{"operation_id": "delete"}, 200, false)
	for _, fn := range []core.Handler{s.download, s.key, s.status} {
		if w = callFile(fn, bob, nil, values, ""); w.Code != 404 {
			t.Fatal("deleted file exposed", w.Code, w.Body)
		}
	}
	if w = callFile(s.status, h.id, nil, values, ""); w.Code != 404 {
		t.Fatal("deleted owner file status exposed", w.Code)
	}
	if err = s.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.path(created.ID, 0)); !os.IsNotExist(err) {
		t.Fatal("deleted chunk retained", err)
	}
	var reserved int64
	h.c.DB.QueryRow(`SELECT COALESCE(SUM(reserved),0) FROM uploads`).Scan(&reserved)
	if reserved != 0 {
		t.Fatal("quota retained", reserved)
	}
}
