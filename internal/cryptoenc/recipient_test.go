package cryptoenc

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestParsedRecipientUsesIndependentEncapsulationsAndBinding(t *testing.T) {
	e := testEngine(t)
	public, _ := base64.StdEncoding.DecodeString(e.PublicKey())
	recipient, err := ParseRecipient(public)
	if err != nil {
		t.Fatal(err)
	}
	// The parsed key must own its state, independent of the caller's buffer.
	clear(public)
	firstBinding := Binding("chat", "user", "device", "first")
	secondBinding := Binding("chat", "user", "device", "second")
	first, err := recipient.SealEnvelope([]byte("hello"), firstBinding)
	if err != nil {
		t.Fatal(err)
	}
	second, err := recipient.SealEnvelope([]byte("hello"), secondBinding)
	if err != nil {
		t.Fatal(err)
	}
	if first.KeyID != e.KeyID() || second.KeyID != e.KeyID() || first.Enc == second.Enc || first.Ciphertext == second.Ciphertext {
		t.Fatal("parsed recipient reused HPKE state or changed key identity")
	}
	for _, input := range []struct {
		envelope Envelope
		binding  []byte
	}{{first, firstBinding}, {second, secondBinding}} {
		plain, err := e.OpenEnvelope(input.envelope, input.binding)
		if err != nil || !bytes.Equal(plain, []byte("hello")) {
			t.Fatal("batch envelope failed to decrypt", err)
		}
	}
	if _, err := e.OpenEnvelope(first, secondBinding); err == nil {
		t.Fatal("message binding was not authenticated")
	}
	if _, err := ParseRecipient([]byte("invalid")); err == nil {
		t.Fatal("invalid recipient key accepted")
	}
}

func BenchmarkRecipientEnvelope(b *testing.B) {
	e, err := New(bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{11}, 32))
	if err != nil {
		b.Fatal(err)
	}
	public, _ := base64.StdEncoding.DecodeString(e.PublicKey())
	plaintext := []byte("hello")
	binding := Binding("chat", "user", "device", "message")
	b.Run("parse-per-message", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := SealEnvelope(public, plaintext, binding); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("parsed-per-batch", func(b *testing.B) {
		recipient, err := ParseRecipient(public)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := recipient.SealEnvelope(plaintext, binding); err != nil {
				b.Fatal(err)
			}
		}
	})
}
