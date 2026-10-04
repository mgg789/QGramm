//go:build qg_openai || qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIConfiguredContextBounds(t *testing.T) {
	cfg := config.Defaults().AI
	cfg.MaxContextTurns = 2
	cfg.MaxContextBytes = 100
	turns := []aiTurn{{Role: "user", Content: "first"}, {Role: "user", Content: "second"}, {Role: "user", Content: "third"}}
	bounded := aiBoundContext(turns, cfg)
	raw, err := json.Marshal(bounded)
	if err != nil || len(bounded) > 2 || len(raw) > 100 || bounded[len(bounded)-1].Content != "third" {
		t.Fatal("configured context bounds did not retain latest input")
	}
	c, cancel := aiFixture(t)
	cancel()
	c.Config.AI = cfg
	calls := 0
	provider := func(context.Context, config.Config, []aiTurn, []config.Tool, aiRequester) (aiAnswer, error) {
		calls++
		return aiAnswer{Text: "answer"}, nil
	}
	if _, err := aiConversationWith(context.Background(), c, "fixture", provider, []aiTurn{{Role: "user", Content: strings.Repeat("x", 101)}}, nil, aiRequest); err == nil || calls != 0 {
		t.Fatal("oversized latest input reached provider")
	}
}

func TestAIConfiguredNetworkAndToolBounds(t *testing.T) {
	settings := config.Defaults().AI
	settings.MaxResponseBytes = 16
	settings.TimeoutSeconds = 2
	ctx := context.WithValue(context.Background(), aiLimitKey{}, aiLimits(settings))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"this exceeds configured limit"}`))
	}))
	defer server.Close()
	if _, err := aiRequest(ctx, server.URL, nil, map[string]any{}, true); err == nil {
		t.Fatal("configured transport response limit not enforced")
	}
	request := aiBoundRequester(func(context.Context, string, map[string]string, any, bool) ([]byte, error) {
		return []byte(strings.Repeat("x", 17)), nil
	})
	if _, err := request(ctx, "https://fixture.invalid", nil, map[string]any{}, false); err == nil {
		t.Fatal("injected requester bypassed configured limit")
	}
	toolCtx, cancel := aiToolContext(ctx, config.Tool{TimeoutSeconds: 1, MaxResponseBytes: 8})
	defer cancel()
	limits := aiContextLimits(toolCtx)
	deadline, ok := toolCtx.Deadline()
	if limits.response != 8 || !ok || time.Until(deadline) > time.Second {
		t.Fatal("tool did not narrow global timeout/response limits")
	}
	inherited, stop := aiToolContext(ctx, config.Tool{TimeoutSeconds: 45, MaxResponseBytes: 1048576})
	defer stop()
	if aiContextLimits(inherited) != aiContextLimits(ctx) {
		t.Fatal("tool enlarged global limits")
	}
	if _, err := request(toolCtx, "https://fixture.invalid", nil, map[string]any{}, false); err == nil {
		t.Fatal("tool response bound bypassed")
	}
}

func TestAIConfiguredNetworkTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()
	settings := config.Defaults().AI
	settings.TimeoutSeconds = 1
	ctx := context.WithValue(context.Background(), aiLimitKey{}, aiLimits(settings))
	start := time.Now()
	if _, err := aiRequest(ctx, server.URL, nil, map[string]any{}, true); err == nil {
		t.Fatal("configured network timeout not enforced")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("configured network timeout exceeded")
	}
}
