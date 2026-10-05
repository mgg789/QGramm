//go:build qg_ai_endpoint && qg_e2ee && qg_openai && qg_http_tools && qg_mcp

package microsafer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func modelRuntime(t *testing.T, modelURL string) *Runtime {
	t.Helper()
	c := testConfig(t, filepath.Join(t.TempDir(), "state.db"))
	c.Models = map[string]ModelConfig{"local": {URL: modelURL, Model: "test-model", MaxBodyBytes: 1 << 20}}
	s, err := OpenStoreWithKeys(c, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New("alice", "alice-device")
	if err != nil {
		t.Fatal(err)
	}
	a.attach(s, "chat")
	return NewRuntime(c, s, a, "chat")
}

func modelRequest() ChatCompletionRequest {
	return ChatCompletionRequest{Messages: []ChatMessage{{Role: "user", Content: "hello"}}}
}

func TestModelStreamRequiresDoneAndUsesUniqueSequence(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":\"a\"}\n\ndata: {\"delta\":\"b\"}\n\ndata: [DONE]\n"))
	}))
	r := modelRuntime(t, server.URL)
	defer r.store.Close()
	t.Setenv(r.cfg.Endpoint.TokenEnv, "relay-secret")
	var got []RPCResponse
	if err := r.CompleteModelStream(context.Background(), "local", modelRequest(), func(resp RPCResponse) error {
		got = append(got, resp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		t.Fatalf("model received relay bearer: %q", authorization)
	}
	if len(got) != 2 || got[0].Seq != 1 || got[0].Kind != "delta" || got[0].Final || got[1].Seq != 2 || got[1].Kind != "result" || !got[1].Final {
		t.Fatalf("unexpected streamed responses: %#v", got)
	}

	missingDone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"delta\":\"truncated\"}\n"))
	}))
	defer missingDone.Close()
	r.cfg.Models["local"] = ModelConfig{URL: missingDone.URL, Model: "test-model", MaxBodyBytes: 1 << 20}
	if err := r.CompleteModelStream(context.Background(), "local", modelRequest(), func(RPCResponse) error { return nil }); err == nil {
		t.Fatal("accepted SSE stream without [DONE]")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n"))
	}))
	defer empty.Close()
	r.cfg.Models["local"] = ModelConfig{URL: empty.URL, Model: "test-model", MaxBodyBytes: 1 << 20}
	var final []RPCResponse
	if err := r.CompleteModelStream(context.Background(), "local", modelRequest(), func(resp RPCResponse) error {
		final = append(final, resp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(final) != 1 || final[0].Kind != "result" || final[0].Seq != 1 || !final[0].Final {
		t.Fatalf("missing empty final response: %#v", final)
	}
}

func TestExternalModelAndToolNeverReceiveRelayBearer(t *testing.T) {
	var modelAuth, toolAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "model") {
			modelAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"delta\":\"ok\"}\n\ndata: [DONE]\n"))
			return
		}
		toolAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"mcp","result":{}}`))
	}))
	defer backend.Close()
	modelURL := "https://external.example/model"
	r := modelRuntime(t, modelURL)
	defer r.store.Close()
	r.cfg.Endpoint.AllowExternalPlaintext = true
	r.cfg.Models["local"] = ModelConfig{URL: modelURL, Model: "test-model", AllowPlaintext: true, MaxBodyBytes: 1 << 20}
	r.transport.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		copyReq := req.Clone(req.Context())
		path := "/model"
		if req.URL.Path == "/mcp" {
			path = "/mcp"
		}
		u, _ := url.Parse(backend.URL + path)
		copyReq.URL = u
		return http.DefaultTransport.RoundTrip(copyReq)
	})}
	t.Setenv(r.cfg.Endpoint.TokenEnv, "relay-secret")
	var modelResponses []RPCResponse
	if err := r.CompleteModelStream(context.Background(), "local", modelRequest(), func(resp RPCResponse) error {
		modelResponses = append(modelResponses, resp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if modelAuth != "" || len(modelResponses) != 2 || modelResponses[0].Kind != "notice" || modelResponses[0].Seq != 1 || modelResponses[1].Seq != 2 {
		t.Fatalf("external model auth/sequence: auth=%q responses=%#v", modelAuth, modelResponses)
	}

	tool, notice, err := r.CallTool(context.Background(), ToolEndpoint{Name: "tool", URL: "https://external.example/mcp", AllowPlaintext: true}, "initialize", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if toolAuth != "" || notice == nil || len(tool) == 0 {
		t.Fatalf("external tool auth/notice: auth=%q notice=%#v response=%s", toolAuth, notice, tool)
	}
}

func TestRuntimeTransportRejectsRedirects(t *testing.T) {
	var followed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			followed = true
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer server.Close()
	r := modelRuntime(t, server.URL)
	defer r.store.Close()
	if _, err := r.transport.Poll(context.Background(), "chat", ""); err == nil {
		t.Fatal("followed or accepted relay redirect")
	}
	if followed {
		t.Fatal("relay transport followed redirect")
	}
}
