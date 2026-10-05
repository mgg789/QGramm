//go:build qg_ai_policy && qg_openai

package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/mgg789/QGramm/internal/config"
)

func TestAIPolicyOpenAIBufferedUsageAndGuard(t *testing.T) {
	previous := aiPolicyActive
	aiPolicyActive = func(context.Context) bool { return true }
	defer func() { aiPolicyActive = previous }()
	c := config.Defaults()
	c.Features.AIPolicy = true
	c.AI.OpenAIKeyEnv = "AI_POLICY_OPENAI_TEST"
	c.AI.Model = "test-model"
	t.Setenv(c.AI.OpenAIKeyEnv, "test-credential")
	request := func(context.Context, string, map[string]string, any, bool) ([]byte, error) {
		return []byte(`{"choices":[{"message":{"content":"answer"}}],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}`), nil
	}
	a, err := aiOpenAI(context.Background(), c, []aiTurn{{Role: "user", Content: "hello"}}, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Usage.Known || a.Usage.InputTokens != 12 || a.Usage.OutputTokens != 4 || a.Usage.CacheReadTokens != 3 {
		t.Fatalf("unexpected OpenAI answer usage: %+v", a.Usage)
	}

	aiPolicyActive = func(context.Context) bool { return false }
	if _, err := aiOpenAI(context.Background(), c, nil, nil, request); err == nil || !strings.Contains(err.Error(), "gateway") {
		t.Fatalf("missing policy gateway was accepted: %v", err)
	}
}
