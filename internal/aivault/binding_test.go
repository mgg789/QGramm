//go:build qg_ai_storage

package aivault

import (
	"bytes"
	"testing"
)

func TestRecordBindingsCannotAliasThroughIDsOrOwnership(t *testing.T) {
	if bytes.Equal(aadDocument("x", "y/document/z"), aadDocument("x/document/y", "z")) {
		t.Fatal("document resource boundary ambiguous")
	}
	if bytes.Equal(aadEdge("x", "y/edge/z"), aadEdge("x/edge/y", "z")) {
		t.Fatal("edge resource boundary ambiguous")
	}
	original := aadResource("resource", ScopeUser, "human", false)
	for _, altered := range [][]byte{aadResource("resource", ScopeShared, "human", false), aadResource("resource", ScopeUser, "other", false), aadResource("resource", ScopeUser, "human", true)} {
		if bytes.Equal(original, altered) {
			t.Fatal("routing/ownership not bound to ciphertext")
		}
	}
}
