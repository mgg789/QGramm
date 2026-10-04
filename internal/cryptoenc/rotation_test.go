package cryptoenc

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestRetiredKeysDecryptOnly(t *testing.T) {
	oldMaster, oldHPKE := bytes.Repeat([]byte{31}, 32), bytes.Repeat([]byte{32}, 32)
	newMaster, newHPKE := bytes.Repeat([]byte{41}, 32), bytes.Repeat([]byte{42}, 32)
	old, err := New(oldMaster, oldHPKE)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := New(newMaster, newHPKE)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewWithPrevious(newMaster, newHPKE, [][]byte{oldMaster}, [][]byte{oldHPKE})
	if err != nil {
		t.Fatal(err)
	}
	aad := Binding("chat", "user", "device", "operation")
	frame, err := old.Seal([]byte("old storage"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.StdEncoding.DecodeString(old.PublicKey())
	envelope, err := SealEnvelope(pub, []byte("old envelope"), aad)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := rotated.Open(frame, aad)
	if err != nil || string(plain) != "old storage" {
		t.Fatal("retired storage decryption failed:", err)
	}
	plain, err = rotated.OpenEnvelope(envelope, aad)
	if err != nil || string(plain) != "old envelope" {
		t.Fatal("retired HPKE decryption failed:", err)
	}
	if _, err = rotated.Open(frame, []byte("wrong aad")); err == nil {
		t.Fatal("retired storage accepted wrong binding")
	}
	if _, err = rotated.OpenEnvelope(envelope, []byte("wrong aad")); err == nil {
		t.Fatal("retired HPKE accepted wrong binding")
	}
	if _, err = primary.Open(frame, aad); err == nil {
		t.Fatal("removed retired master still decrypts")
	}
	if _, err = primary.OpenEnvelope(envelope, aad); err == nil {
		t.Fatal("removed retired HPKE still decrypts")
	}
	if rotated.PublicKey() != primary.PublicKey() || rotated.KeyID() != primary.KeyID() {
		t.Fatal("rotation advertised retired primary")
	}
	newFrame, err := rotated.Seal([]byte("new storage"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = primary.Open(newFrame, aad); err != nil {
		t.Fatal("new storage does not use primary:", err)
	}
	if _, err = old.Open(newFrame, aad); err == nil {
		t.Fatal("new storage used retired key")
	}
	pub, _ = base64.StdEncoding.DecodeString(rotated.PublicKey())
	newEnvelope, err := SealEnvelope(pub, []byte("new envelope"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if newEnvelope.KeyID != primary.KeyID() {
		t.Fatal("new envelope has retired key ID")
	}
	if _, err = primary.OpenEnvelope(newEnvelope, aad); err != nil {
		t.Fatal("new envelope does not use primary:", err)
	}
	if _, err = old.OpenEnvelope(newEnvelope, aad); err == nil {
		t.Fatal("old HPKE decrypts new envelope")
	}
	newEnvelope.KeyID = old.KeyID()
	if _, err = rotated.OpenEnvelope(newEnvelope, aad); err == nil {
		t.Fatal("key ID substitution accepted")
	}
}

func TestRetiredKeyLimitsAndValidation(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	for _, count := range []int{0, 4} {
		keys := make([][]byte, count)
		for i := range keys {
			keys[i] = key
		}
		if _, err := NewWithPrevious(key, key, keys, keys); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct{ masters, hpke [][]byte }{
		{make([][]byte, 5), nil}, {nil, make([][]byte, 5)},
		{[][]byte{nil}, nil}, {nil, [][]byte{nil}},
		{[][]byte{make([]byte, 31)}, nil}, {nil, [][]byte{make([]byte, 33)}},
	}
	for i, test := range cases {
		if _, err := NewWithPrevious(key, key, test.masters, test.hpke); err == nil {
			t.Fatalf("accepted invalid retired keys case %d", i)
		}
	}
}
