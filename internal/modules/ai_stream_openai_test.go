//go:build qg_ai_streaming && qg_openai

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
)

func TestOpenAIStreamIncrementalAndToolAssembly(t *testing.T) {
	deltaSeen := make(chan struct{})
	var deltaOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Stream {
			t.Errorf("stream request flag missing")
			return
		}
		writeSSE(w, "", `{"choices":[{"delta":{"content":"hel"}}]}`)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		writeSSE(w, "", `{"choices":[{"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call-1","function":{"name":"query","arguments":"{\"q\":"}}]}}]}`)
		writeSSE(w, "", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}`)
		writeSSE(w, "", `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
		writeSSE(w, "", "[DONE]")
	}))
	defer server.Close()
	c := config.Defaults()
	c.AI.OpenAIURL = server.URL
	c.AI.OpenAIKeyEnv = "AI_STREAM_OPENAI_TEST"
	c.AI.Model = "test-model"
	c.AI.AllowPrivate = true
	t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var events []string
	var eventsMu sync.Mutex
	answerCh := make(chan struct {
		a aiAnswer
		e error
	}, 1)
	go func() {
		a, e := aiOpenAIStream(ctx, c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(kind string, payload json.RawMessage) error {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, kind+":"+string(payload))
			deltaOnce.Do(func() { close(deltaSeen) })
			return nil
		})
		answerCh <- struct {
			a aiAnswer
			e error
		}{a, e}
	}()
	select {
	case <-deltaSeen:
	case <-time.After(time.Second):
		t.Fatal("provider did not expose first incremental event")
	}
	eventsMu.Lock()
	if len(events) != 1 || !strings.HasPrefix(events[0], "text.delta:") {
		eventsMu.Unlock()
		t.Fatalf("first delta was not exposed before completion: %v", events)
	}
	eventsMu.Unlock()
	releaseOnce.Do(func() { close(release) })
	result := <-answerCh
	if result.e != nil {
		t.Fatal(result.e)
	}
	if result.a.Text != "hello" || len(result.a.Calls) != 1 || result.a.Calls[0].ID != "call-1" || result.a.Calls[0].Name != "query" || string(result.a.Calls[0].Arguments) != `{"q":"x"}` {
		t.Fatalf("assembled answer %+v", result.a)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 5 || !strings.HasPrefix(events[2], "tool.started:") || !strings.HasPrefix(events[3], "tool.arguments.delta:") || !strings.HasPrefix(events[4], "tool.arguments.delta:") {
		t.Fatalf("unexpected stream events %v", events)
	}
}

func TestOpenAIStreamCancellationClosesRequest(t *testing.T) {
	seen := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, "", `{"choices":[{"delta":{"content":"partial"}}]}`)
		<-r.Context().Done()
		close(seen)
	}))
	defer server.Close()
	c := config.Defaults()
	c.AI.OpenAIURL = server.URL
	c.AI.OpenAIKeyEnv = "AI_STREAM_CANCEL_TEST"
	c.AI.Model = "test-model"
	c.AI.AllowPrivate = true
	t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := aiOpenAIStream(ctx, c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(string, json.RawMessage) error {
			cancel()
			return nil
		})
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancellation accepted as a completed provider response")
		}
	case <-time.After(time.Second):
		t.Fatal("provider did not stop after cancellation")
	}
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("request body remained open after cancellation")
	}
}

func TestOpenAIStreamRejectsInvalidToolIndices(t *testing.T) {
	for name, firstEvent := range map[string]string{
		"huge":   `{"choices":[{"delta":{"tool_calls":[{"index":8,"id":"call-1","function":{"name":"query","arguments":"{}"}}]}}]}`,
		"sparse": `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call-1","function":{"name":"query","arguments":"{}"}}]}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeSSE(w, "", firstEvent)
				if name == "sparse" {
					writeSSE(w, "", `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
					writeSSE(w, "", "[DONE]")
				}
			}))
			defer server.Close()
			c := config.Defaults()
			c.AI.OpenAIURL = server.URL
			c.AI.OpenAIKeyEnv = "AI_STREAM_INDEX_TEST"
			c.AI.Model = "test-model"
			c.AI.AllowPrivate = true
			t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := aiOpenAIStream(ctx, c, []aiTurn{{Role: "user", Content: "hello"}}, nil, func(string, json.RawMessage) error { return nil })
			if err == nil {
				t.Fatal("accepted invalid tool call index")
			}
		})
	}
}
