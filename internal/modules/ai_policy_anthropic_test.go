//go:build qg_ai_policy && qg_anthropic

package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIPolicyAnthropicBufferedUsageAndGuard(t *testing.T) {
	previous := aiPolicyActive
	aiPolicyActive = func(context.Context) bool { return true }
	defer func() { aiPolicyActive = previous }()
	c := config.Defaults()
	c.Features.AIPolicy = true
	c.AI.AnthropicKeyEnv = "AI_POLICY_ANTHROPIC_TEST"
	c.AI.Model = "test-model"
	t.Setenv(c.AI.AnthropicKeyEnv, "test-credential")
	request := func(context.Context, string, map[string]string, any, bool) ([]byte, error) {
		return []byte(`{"content":[{"type":"text","text":"answer"}],"usage":{"input_tokens":100,"cache_creation_input_tokens":4,"cache_read_input_tokens":6,"output_tokens":7}}`), nil
	}
	a, err := aiAnthropic(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Usage.Known || a.Usage.InputTokens != 110 || a.Usage.OutputTokens != 7 || a.Usage.CacheReadTokens != 6 || a.Usage.CacheWriteTokens != 4 {
		t.Fatalf("unexpected Anthropic answer usage: %+v", a.Usage)
	}

	aiPolicyActive = func(context.Context) bool { return false }
	if _, err := aiAnthropic(context.Background(), c, nil, nil, request); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("missing policy gateway was accepted: %v", err)
	}
}
