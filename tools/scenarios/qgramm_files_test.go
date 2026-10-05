package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestQFileChunkAuthenticationAndBytes(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	plain := filePlainChunk(1, 2, 1<<20)
	binding := cryptoenc.Binding("c", "owner", "device", "file/2")
	wire, err := sealFileChunk(aead, plain, binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != len(plain)+28 {
		t.Fatalf("wire size %d", len(wire))
	}
	restored, err := openFileChunk(aead, wire, binding)
	if err != nil || !bytes.Equal(plain, restored) || sha256.Sum256(plain) != sha256.Sum256(restored) {
		t.Fatalf("roundtrip: %v", err)
	}
	if _, err = openFileChunk(aead, wire, cryptoenc.Binding("c", "owner", "device", "file/3")); err == nil {
		t.Fatal("chunk index not authenticated")
	}
	wire[len(wire)-1] ^= 1
	if _, err = openFileChunk(aead, wire, binding); err == nil {
		t.Fatal("corrupt chunk accepted")
	}
	if _, err = openFileChunk(aead, wire[:3], binding); err == nil {
		t.Fatal("truncated chunk accepted")
	}
}

func TestQFileKeyEnvelopeBinding(t *testing.T) {
	recipient := testQEngine(t)
	public := recipient.PublicKey()
	key := make([]byte, 32)
	rand.Read(key)
	// The endpoint wraps the key with upload ID, while chunk AAD retains the
	// creator's operation/device. Using the operation to unwrap must fail.
	publicBytes, err := base64.StdEncoding.DecodeString(public)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := cryptoenc.SealEnvelope(publicBytes, key, cryptoenc.Binding("chat", "recipient", "recipient-device", "upload-id"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := recipient.OpenEnvelope(envelope, cryptoenc.Binding("chat", "recipient", "recipient-device", "upload-id"))
	if err != nil || !bytes.Equal(opened, key) {
		t.Fatal("recipient key unwrap failed")
	}
	if _, err = recipient.OpenEnvelope(envelope, cryptoenc.Binding("chat", "recipient", "recipient-device", "creator-operation")); err == nil {
		t.Fatal("wrong key unwrap binding accepted")
	}
}
