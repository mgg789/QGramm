package cryptoenc

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/cloudflare/circl/hpke"
	"os"
	"testing"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestStorageAEAD(t *testing.T) {
	e := testEngine(t)
	aad := Binding("chat", "user", "device", "message")
	a, err := e.Seal([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := e.Seal([]byte("secret"), aad)
	if bytes.Equal(a, b) {
		t.Fatal("reused nonce")
	}
	pt, err := e.Open(a, aad)
	if err != nil || string(pt) != "secret" {
		t.Fatal(err)
	}
	if _, err = e.Open(a, []byte("different")); err == nil {
		t.Fatal("accepted wrong AAD")
	}
	for i := range a {
		bad := bytes.Clone(a)
		bad[i] ^= 1
		if _, err = e.Open(bad, aad); err == nil {
			t.Fatalf("accepted modification at %d", i)
		}
	}
	for i := 0; i < len(a); i++ {
		if _, err = e.Open(a[:i], aad); err == nil {
			t.Fatalf("accepted truncated frame %d", i)
		}
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32), bytes.Repeat([]byte{12}, 32))
	if _, err = other.Open(a, aad); err == nil {
		t.Fatal("accepted wrong key")
	}
}
func TestEnvelope(t *testing.T) {
	e := testEngine(t)
	pub, _ := base64.StdEncoding.DecodeString(e.PublicKey())
	aad := Binding("c", "u", "d", "send")
	env, err := SealEnvelope(pub, []byte("hello"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := e.OpenEnvelope(env, aad)
	if err != nil || string(pt) != "hello" {
		t.Fatal(err)
	}
	if _, err = e.OpenEnvelope(env, Binding("other", "u", "d", "send")); err == nil {
		t.Fatal("accepted wrong chat")
	}
	bad := env
	ct, _ := base64.StdEncoding.DecodeString(bad.Ciphertext)
	ct[0] ^= 1
	bad.Ciphertext = base64.StdEncoding.EncodeToString(ct)
	if _, err = e.OpenEnvelope(bad, aad); err == nil {
		t.Fatal("accepted corrupted envelope")
	}
	bad = env
	bad.KeyID = "unknown"
	if _, err = e.OpenEnvelope(bad, aad); err == nil {
		t.Fatal("accepted unknown key")
	}
	if _, err = SealEnvelope(make([]byte, 32), []byte("x"), aad); err == nil {
		t.Fatal("accepted low-order public key")
	}
}
func TestBindingCanonical(t *testing.T) {
	want := `{"version":1,"chat":"c","user":"u","device":"d","operation":"send"}`
	if string(Binding("c", "u", "d", "send")) != want {
		t.Fatal("binding changed")
	}
}
func TestKeyValidation(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		if _, err := New(make([]byte, n), make([]byte, 32)); err == nil {
			t.Fatal("accepted invalid master")
		}
		if _, err := New(make([]byte, 32), make([]byte, n)); err == nil {
			t.Fatal("accepted invalid private")
		}
	}
}

// CFRG RFC 9180 test vector, base mode KEM=32/KDF=1/AEAD=1.
// Source: https://github.com/cfrg/draft-irtf-cfrg-hpke/blob/master/test-vectors.json
// Plaintext, key material, and deterministic entropy here are public test values.
func TestRFC9180Vector(t *testing.T) {
	var v struct {
		Info, IkmE, SkRm, PkRm, Enc string
		Encryptions                 []struct{ AAD, PT, CT string }
	}
	raw, err := os.ReadFile("testdata/rfc9180-base-x25519.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	dec := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	scheme := hpke.KEM_X25519_HKDF_SHA256.Scheme()
	public, err := scheme.UnmarshalBinaryPublicKey(dec(v.PkRm))
	if err != nil {
		t.Fatal(err)
	}
	private, err := scheme.UnmarshalBinaryPrivateKey(dec(v.SkRm))
	if err != nil {
		t.Fatal(err)
	}
	sender, err := suite.NewSender(public, dec(v.Info))
	if err != nil {
		t.Fatal(err)
	}
	enc, sealer, err := sender.Setup(bytes.NewReader(dec(v.IkmE)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc, dec(v.Enc)) {
		t.Fatal("enc differs from RFC vector")
	}
	receiver, err := suite.NewReceiver(private, dec(v.Info))
	if err != nil {
		t.Fatal(err)
	}
	opener, err := receiver.Setup(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Encryptions) == 0 {
		t.Fatal("empty vector")
	}
	for i, vector := range v.Encryptions {
		ct, err := sealer.Seal(dec(vector.PT), dec(vector.AAD))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(ct, dec(vector.CT)) {
			t.Fatalf("RFC ciphertext mismatch %d", i)
		}
		pt, err := opener.Open(ct, dec(vector.AAD))
		if err != nil || !bytes.Equal(pt, dec(vector.PT)) {
			t.Fatalf("RFC open mismatch %d: %v", i, err)
		}
	}
}
