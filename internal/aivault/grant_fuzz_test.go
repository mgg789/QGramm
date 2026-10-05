//go:build qg_ai_storage

package aivault

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func FuzzStorageGrant(f *testing.F) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	signer, err := NewGrantSigner(private, "backend", "storage", time.Minute)
	if err != nil {
		f.Fatal(err)
	}
	token, err := signer.Sign(Claims{Nonce: "nonce", RequestID: "request", RequestHash: "hash", ChatID: "chat", SourceUser: "human", AIUser: "bot", Resource: "knowledge", Action: ActionRead, Destination: "https://model.example/v1", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(token)
	f.Add("eyJhbGciOiJFZERTQSIsInR5cCI6IkpXVCJ9.e30.bad")
	f.Add("invalid")
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 16384 {
			return
		}
		claims, err := Verify(public, "backend", "storage", time.Minute, input)
		if err == nil && (claims.Issuer != "backend" || claims.Audience != "storage" || claims.ExpiresAt <= claims.IssuedAt || claims.Resource == "") {
			t.Fatal("accepted grant violated verifier contract")
		}
	})
}
