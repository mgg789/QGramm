//go:build qg_ai_storage

package aivault

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
	_ "modernc.org/sqlite"
)

func testStore(t *testing.T, limits Limits) (*Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:aivault-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	engine, err := cryptoenc.New(bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(db, engine, limits)
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestEncryptedPayloadAndTamper(t *testing.T) {
	s, db := testStore(t, Limits{})
	r, err := s.CreateResource(context.Background(), ResourceSpec{ID: "r1", Scope: ScopeUser, Owner: "u1", Metadata: json.RawMessage(`{"private":"secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.PutDocument(context.Background(), r.ID, DocumentInput{ID: "d1", Text: "the private phrase", File: []byte("blob")})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(`SELECT payload FROM aivault_documents WHERE id='d1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private phrase")) || bytes.Contains(raw, []byte("blob")) {
		t.Fatal("plaintext document leaked to database")
	}
	if _, err := s.GetDocument(context.Background(), r.ID, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE aivault_documents SET payload=? WHERE id='d1'`, append(raw[:len(raw)-1], raw[len(raw)-1]^1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDocument(context.Background(), r.ID, d.ID); err != ErrCorrupt {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestCrossResourceSearchAndQuotas(t *testing.T) {
	s, _ := testStore(t, Limits{MaxDocuments: 1, MaxVectorDimensions: 2, MaxSearchResults: 1})
	ctx := context.Background()
	first, err := s.CreateResource(ctx, ResourceSpec{ID: "a", Scope: ScopeShared, Owner: "group"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateResource(ctx, ResourceSpec{ID: "b", Scope: ScopeUser, Owner: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutDocument(ctx, first.ID, DocumentInput{ID: "da", Text: "alpha", Vector: []float64{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PutDocument(ctx, first.ID, DocumentInput{ID: "overflow", Text: "beta"}); err != ErrQuota {
		t.Fatalf("quota error = %v", err)
	}
	if _, err = s.PutDocument(ctx, second.ID, DocumentInput{ID: "db", Text: "alpha", Vector: []float64{0, 1}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, first.ID, SearchQuery{Text: "alpha", Vector: []float64{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Resource.ID != first.ID || got[0].Resource.Scope != ScopeShared || got[0].Resource.Owner != "group" {
		t.Fatalf("unexpected provenance: %+v", got)
	}
	if got[0].Document.File != nil || got[0].Document.Vector != nil {
		t.Fatal("search returned file/vector payload")
	}
	if _, err = s.Search(ctx, first.ID, SearchQuery{Vector: []float64{1, 2, 3}}); err != ErrQuota {
		t.Fatalf("vector bound error = %v", err)
	}
}

func TestGraphBFSBounded(t *testing.T) {
	s, _ := testStore(t, Limits{MaxBFSDepth: 2, MaxSearchResults: 2})
	ctx := context.Background()
	r, err := s.CreateResource(ctx, ResourceSpec{ID: "g", Scope: ScopeBot, Owner: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range []EdgeInput{{ID: "e1", From: "a", To: "b"}, {ID: "e2", From: "b", To: "c"}, {ID: "e3", From: "c", To: "d"}} {
		if _, err = s.PutEdge(ctx, r.ID, e); err != nil {
			t.Fatalf("edge %d: %v", i, err)
		}
	}
	g, err := s.GraphBFS(ctx, r.ID, GraphQuery{Start: "a", Depth: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Edges) != 2 || g.Resource.ID != r.ID {
		t.Fatalf("unexpected BFS: %+v", g)
	}
	if _, err = s.GraphBFS(ctx, r.ID, GraphQuery{Start: "a", Depth: 3}); err != ErrQuota {
		t.Fatalf("depth bound error = %v", err)
	}
}

func TestNewRejectsFutureSchemaAndNegativeLimits(t *testing.T) {
	s, db := testStore(t, Limits{})
	_ = s
	if _, err := db.Exec(`INSERT INTO aivault_schema(version) VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	engine, err := cryptoenc.New(bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(db, engine, Limits{}); err == nil {
		t.Fatal("future schema version accepted")
	}
	if _, err = New(db, engine, Limits{MaxEdges: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestStrictGrant(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := NewGrantSigner(priv, "issuer", "aud", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims := Claims{Nonce: "n", RequestID: "req", RequestHash: "hash", ChatID: "chat", SourceUser: "u", AIUser: "bot", Resource: "r", Action: ActionSearch, Destination: "https://provider"}
	token, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Grant{}).Verify(pub, "issuer", "aud", time.Minute, token)
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "req" || got.Resource != "r" {
		t.Fatalf("claims lost: %+v", got)
	}
	if err = got.Authorize(RetrievalRequest{Action: ActionSearch, ResourceID: "other", Destination: "https://provider"}); err == nil {
		t.Fatal("cross-resource authorization accepted")
	}
	parts := strings.Split(token, ".")
	var body map[string]json.RawMessage
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(b, &body)
	body["extra"] = json.RawMessage(`"x"`)
	b, _ = json.Marshal(body)
	parts[1] = base64.RawURLEncoding.EncodeToString(b)
	if _, err = (Grant{}).Verify(pub, "issuer", "aud", time.Minute, strings.Join(parts, ".")); err == nil {
		t.Fatal("unknown claim accepted")
	}
}
