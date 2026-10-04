//go:build qg_e2ee

package cryptoenc

import (
	"bytes"
	"context"
	mls "github.com/thomas-vilte/mls-go"
	"github.com/thomas-vilte/mls-go/ciphersuite"
	"testing"
)

// This checks adapter framing against the library's public Client API. It is
// intentionally not described as interoperability with an independent implementation.
func TestMLSClientAPIInterop(t *testing.T) {
	ctx := context.Background()
	client, err := mls.NewClient([]byte("peer"), ciphersuite.MLS128DHKEMX25519)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	agent, err := NewMLS([]byte("agent"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := agent.CreateGroup()
	if err != nil {
		t.Fatal(err)
	}
	kp, err := client.FreshKeyPackageBytes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, welcome, err := agent.Invite(kp)
	if err != nil {
		t.Fatal(err)
	}
	joined, err := client.JoinGroup(ctx, welcome)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(id, joined) {
		t.Fatal("group ID changed")
	}
	wire, err := agent.Encrypt([]byte("agent message"), nil)
	if err != nil {
		t.Fatal(err)
	}
	received, err := client.ReceiveMessage(ctx, id, wire)
	if err != nil {
		t.Fatal(err)
	}
	if string(received.Plaintext) != "agent message" {
		t.Fatal("wrong client plaintext")
	}
	wire, err = client.SendMessage(ctx, id, []byte("peer message"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := agent.Decrypt(wire, nil)
	if err != nil || string(pt) != "peer message" {
		t.Fatal(err)
	}
	third, err := NewMLS([]byte("third"))
	if err != nil {
		t.Fatal(err)
	}
	commit, welcome, err := client.InviteMember(ctx, id, third.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	corrupted := bytes.Clone(commit)
	corrupted[len(corrupted)-1] ^= 1
	if err = agent.ProcessCommit(corrupted); err == nil {
		t.Fatal("accepted corrupt commit")
	}
	if err = agent.ProcessCommit(commit); err != nil {
		t.Fatal(err)
	}
	if _, err = third.Join(welcome); err != nil {
		t.Fatal(err)
	}
	wire, err = third.Encrypt([]byte("third message"), nil)
	if err != nil {
		t.Fatal(err)
	}
	pt, err = agent.Decrypt(wire, nil)
	if err != nil || string(pt) != "third message" {
		t.Fatal(err)
	}
}

func TestMLSLifecycleRestoreCommit(t *testing.T) {
	e := testEngine(t)
	alice, err := NewMLS([]byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := NewMLS([]byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = alice.CreateGroup(); err != nil {
		t.Fatal(err)
	}
	// Pending KeyPackage survives restart before Welcome arrives.
	state, err := bob.Snapshot(e, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err = RestoreMLS(e, state, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	_, welcome, err := alice.Invite(bob.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bob.Join(welcome); err != nil {
		t.Fatal(err)
	}
	aad := []byte("chat binding")
	wire, err := alice.Encrypt([]byte("one"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bob.Decrypt(wire, []byte("wrong")); err == nil {
		t.Fatal("accepted wrong binding")
	}
	pt, err := bob.Decrypt(wire, aad)
	if err != nil || string(pt) != "one" {
		t.Fatalf("first message: %v", err)
	}
	if _, err = bob.Decrypt(wire, aad); err == nil {
		t.Fatal("accepted replay")
	}
	receiverSnapshot, err := bob.Snapshot(e, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err = RestoreMLS(e, receiverSnapshot, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = bob.Decrypt(wire, aad); err == nil {
		t.Fatal("accepted replay after restore")
	}
	if _, err = bob.Decrypt(append(bytes.Clone(wire), 0), aad); err == nil {
		t.Fatal("accepted appended-byte replay after restore")
	}
	// Persist advanced sender counters before restart; restored sender must
	// remain interoperable with a receiver that already consumed generation 0.
	saved, err := alice.Snapshot(e, []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	alice, err = RestoreMLS(e, saved, []byte("alice"))
	if err != nil {
		t.Fatal(err)
	}
	wire2, err := alice.Encrypt([]byte("two"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(wire, wire2) {
		t.Fatal("repeated ciphertext")
	}
	pt, err = bob.Decrypt(wire2, aad)
	if err != nil || string(pt) != "two" {
		t.Fatalf("restored sender: %v", err)
	}
	charlie, err := NewMLS([]byte("charlie"))
	if err != nil {
		t.Fatal(err)
	}
	commit, welcome, err := alice.Invite(charlie.KeyPackage())
	if err != nil {
		t.Fatal(err)
	}
	if err = bob.ProcessCommit(commit); err != nil {
		t.Fatal(err)
	}
	if _, err = charlie.Join(welcome); err != nil {
		t.Fatal(err)
	}
	wire, err = charlie.Encrypt([]byte("epoch advanced"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err = bob.Decrypt(wire, aad)
	if err != nil || string(pt) != "epoch advanced" {
		t.Fatalf("commit: %v", err)
	}
	saved, err = bob.Snapshot(e, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	bob, err = RestoreMLS(e, saved, []byte("bob"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err = alice.Encrypt([]byte("three"), aad)
	if err != nil {
		t.Fatal(err)
	}
	pt, err = bob.Decrypt(wire, aad)
	if err != nil || string(pt) != "three" {
		t.Fatalf("restored receiver: %v", err)
	}
	if _, err = RestoreMLS(e, saved, []byte("alice")); err == nil {
		t.Fatal("accepted snapshot for wrong identity")
	}
}
