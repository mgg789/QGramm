//go:build qg_files

package modules

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/mgg789/QGramm/internal/cryptoenc"
	"testing"
)

// Includes HPKE key admission, authenticated streaming chunk persistence/fsync
// and checksum completion. SQLite fixture is in-memory; this is not HTTP load.
func BenchmarkEncryptedFileUpload64KiB(b *testing.B) {
	s, id := fileFixture(b)
	session := make([]byte, 32)
	rand.Read(session)
	block, _ := aes.NewCipher(session)
	aead, _ := cipher.NewGCM(block)
	public, _ := base64.StdEncoding.DecodeString(s.c.Engine.PublicKey())
	plain := bytes.Repeat([]byte("x"), 64<<10)
	b.SetBytes(int64(len(plain)))
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		operation := fmt.Sprintf("bench-%d", n)
		nonce := make([]byte, aead.NonceSize())
		rand.Read(nonce)
		wire := aead.Seal(append([]byte{}, nonce...), nonce, plain, cryptoenc.Binding("chat", id.UserID, id.DeviceID, operation+"/0"))
		sum := sha256.Sum256(wire)
		digest := hex.EncodeToString(sum[:])
		env, e := cryptoenc.SealEnvelope(public, session, cryptoenc.Binding("chat", id.UserID, id.DeviceID, operation))
		if e != nil {
			b.Fatal(e)
		}
		body, _ := json.Marshal(map[string]any{"operation_id": operation, "size": len(wire), "chunks": 1, "sha256": digest, "envelope": env})
		w := callFile(s.create, id, body, map[string]string{"chat": "chat"}, "")
		if w.Code != 201 {
			b.Fatal("create", w.Code)
		}
		var upload struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(w.Body.Bytes(), &upload) != nil {
			b.Fatal("upload response")
		}
		values := map[string]string{"upload": upload.ID, "index": "0"}
		if w = callFile(s.chunk, id, wire, values, digest); w.Code != 201 {
			b.Fatal("chunk", w.Code)
		}
		if w = callFile(s.complete, id, nil, values, ""); w.Code != 200 {
			b.Fatal("complete", w.Code)
		}
	}
}
