//go:build qg_ai_storage && qg_ai_policy && qg_http_tools && qg_groups && qg_delete && qg_edit && qg_reactions && qg_reply && qg_forward && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mgg789/QGramm/internal/aivault"
	"github.com/mgg789/QGramm/internal/core"
)

func storageResponse(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode storage response: %v (%s)", err, raw)
	}
	return out
}

func TestAIStorageManagementBearerCRUDTamperAndScope(t *testing.T) {
	h := newModuleHarness(t, "global")
	resource := map[string]any{"scope": "user", "owner": "alice", "metadata": map[string]any{"kind": "private"}}
	// A device token must not acquire the administrative storage authority.
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-a", resource, 401, false)
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-a", resource, 200, true)
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-b", map[string]any{"scope": "shared", "owner": "group"}, 200, true)

	secretFile := []byte("encrypted file fixture")
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-a/documents/doc-a", map[string]any{
		"text":     "private retrieval fact",
		"vector":   []float64{1, 0},
		"filename": "private.txt",
		"file":     secretFile,
		"metadata": map[string]any{"label": "private"},
	}, 200, true)
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-b/documents/doc-b", map[string]any{
		"text":   "another resource",
		"vector": []float64{0, 1},
	}, 200, true)
	h.request(t, "PUT", "/management/v1/ai/storage/resources/private-a/edges/edge-a", map[string]any{
		"from": "doc-a", "to": "doc-b", "label": "related", "vector": []float64{1, 1},
	}, 200, true)

	got := storageResponse(t, h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 200, true))
	doc, ok := got["document"].(map[string]any)
	if !ok || doc["text"] != "private retrieval fact" || doc["filename"] != "private.txt" {
		t.Fatalf("document payload/provenance missing: %#v", got)
	}
	if got["resource"] == nil || got["resource"].(map[string]any)["owner"] != "alice" {
		t.Fatalf("resource ownership provenance missing: %#v", got["resource"])
	}

	// The routing/ownership header is authenticated before the document
	// payload is opened. A tampered scope/owner header therefore rejects the
	// whole retrieval instead of exposing document content.
	var sealedResource []byte
	if err := h.c.DB.QueryRow(`SELECT payload FROM aivault_resources WHERE id='private-a'`).Scan(&sealedResource); err != nil {
		t.Fatal(err)
	}
	tamperedResource := append(append([]byte(nil), sealedResource...), 0x01)
	if _, err := h.c.DB.Exec(`UPDATE aivault_resources SET payload=? WHERE id='private-a'`, tamperedResource); err != nil {
		t.Fatal(err)
	}
	h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 503, true)
	if _, err := h.c.DB.Exec(`UPDATE aivault_resources SET payload=? WHERE id='private-a'`, sealedResource); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`UPDATE aivault_resources SET scope='shared' WHERE id='private-a'`, `UPDATE aivault_resources SET owner='bob' WHERE id='private-a'`, `UPDATE aivault_resources SET routing_visible=1 WHERE id='private-a'`} {
		if _, err := h.c.DB.Exec(statement); err != nil {
			t.Fatal(err)
		}
		h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 503, true)
		if _, err := h.c.DB.Exec(`UPDATE aivault_resources SET scope='user',owner='alice',routing_visible=0 WHERE id='private-a'`); err != nil {
			t.Fatal(err)
		}
	}

	var sealed []byte
	if err := h.c.DB.QueryRow(`SELECT payload FROM aivault_documents WHERE id='doc-a'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("private retrieval fact")) || bytes.Contains(sealed, secretFile) || bytes.Contains(sealed, []byte("private.txt")) {
		t.Fatal("document ciphertext contains plaintext content")
	}
	if _, err := h.c.DB.Exec(`UPDATE aivault_documents SET payload=? WHERE id='doc-a'`, append(append([]byte(nil), sealed...), 0x01)); err != nil {
		t.Fatal(err)
	}
	h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 503, true)

	// The resource qualifier is part of every storage lookup; a document in a
	// different resource must not be reachable by changing only the path.
	h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-b", nil, 404, true)
	h.request(t, "DELETE", "/management/v1/ai/storage/resources/private-a/edges/edge-a", nil, 200, true)
	h.request(t, "DELETE", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 200, true)
	h.request(t, "DELETE", "/management/v1/ai/storage/resources/private-a", nil, 200, true)
	h.request(t, "GET", "/management/v1/ai/storage/resources/private-a/documents/doc-a", nil, 404, true)
}

func TestAIStorageAdministrativeMCPReadSearchGraphAndBadArgs(t *testing.T) {
	h := newModuleHarness(t, "global")
	h.request(t, "PUT", "/management/v1/ai/storage/resources/mcp-r", map[string]any{"scope": "shared", "owner": "group"}, 200, true)
	for id, text := range map[string]string{"node-a": "alpha fact", "node-b": "beta fact"} {
		h.request(t, "PUT", "/management/v1/ai/storage/resources/mcp-r/documents/"+id, map[string]any{"text": text, "vector": []float64{1, 0}}, 200, true)
	}
	h.request(t, "PUT", "/management/v1/ai/storage/resources/mcp-r/edges/edge-ab", map[string]any{"from": "node-a", "to": "node-b", "label": "next"}, 200, true)
	h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}}}, 200, true)
	list := storageResponse(t, h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{}}, 200, true))
	result, ok := list["result"].(map[string]any)
	if !ok || len(result["tools"].([]any)) != 3 {
		t.Fatalf("MCP tools/list contract: %#v", list)
	}
	read := storageResponse(t, h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "read", "arguments": map[string]any{"resource": "mcp-r", "document_id": "node-a"}}}, 200, true))
	if !strings.Contains(mcpText(t, read), "alpha fact") {
		t.Fatalf("MCP read did not return document: %#v", read)
	}
	search := storageResponse(t, h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "search", "arguments": map[string]any{"resource": "mcp-r", "query": "alpha", "limit": 1}}}, 200, true))
	if !strings.Contains(mcpText(t, search), "node-a") || !strings.Contains(mcpText(t, search), "alpha fact") {
		t.Fatalf("MCP search did not return scoped result: %#v", search)
	}
	graph := storageResponse(t, h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/call", "params": map[string]any{"name": "graph", "arguments": map[string]any{"resource": "mcp-r", "start": "node-a", "depth": 2, "limit": 1}}}, 200, true))
	if !strings.Contains(mcpText(t, graph), "node-b") || !strings.Contains(mcpText(t, graph), "edge-ab") {
		t.Fatalf("MCP graph did not return bounded topology: %#v", graph)
	}
	badRPC := map[string]any{"jsonrpc": "1.0", "id": 6, "method": "tools/list", "params": map[string]any{}}
	h.request(t, "POST", "/management/v1/ai/storage/mcp", badRPC, 400, true)
	badArgs := storageResponse(t, h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "search", "arguments": map[string]any{"resource": "mcp-r", "query": "alpha", "unexpected": true}}}, 200, true))
	if badArgs["result"].(map[string]any)["isError"] != true {
		t.Fatalf("MCP unknown argument was accepted: %#v", badArgs)
	}
	h.request(t, "POST", "/management/v1/ai/storage/mcp", map[string]any{"jsonrpc": "2.0", "id": 8, "method": "tools/list", "params": map[string]any{}}, 401, false)
}

func mcpText(t *testing.T, response map[string]any) string {
	t.Helper()
	result, ok := response["result"].(map[string]any)
	if !ok {
		t.Fatalf("MCP result missing: %#v", response)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("MCP content missing: %#v", result)
	}
	item := content[0].(map[string]any)
	return item["text"].(string)
}

func TestAIStorageGrantVerifierRejectsExpiredAndWrongAudience(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	claims := map[string]any{"nonce": "nonce", "request_id": "request", "request_hash": "hash", "chat_id": "chat", "source_user": "alice", "ai_user": "bot", "resource": "resource", "action": string(aivault.ActionSearch), "destination": "https://llm.example", "iss": "issuer", "aud": "audience", "iat": now - 120, "exp": now - 60}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims(claims)).SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = aivault.Verify(public, "issuer", "audience", time.Minute, expired); err == nil {
		t.Fatal("expired storage grant accepted")
	}
	claims["iat"] = now
	claims["exp"] = now + 30
	wrongAudience, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims(claims)).SignedString(private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = aivault.Verify(public, "issuer", "other-audience", time.Minute, wrongAudience); err == nil {
		t.Fatal("storage grant with wrong audience accepted")
	}
}

func TestAIStorageMigrationRejectsUnknownVersionOnSecondCore(t *testing.T) {
	h := newModuleHarness(t, "global")
	if _, err := h.c.DB.Exec(`INSERT INTO aivault_schema(version) VALUES(2)`); err != nil {
		t.Fatal(err)
	}
	second := &core.Core{DB: h.c.DB, Engine: h.c.Engine, Config: h.c.Config, Context: context.Background(), Mux: http.NewServeMux()}
	if err := installAIStorage(second); err == nil {
		t.Fatal("unknown AI storage schema version accepted")
	}
}
