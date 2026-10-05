//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import "testing"

func TestAIPolicyUsageOpenAIBufferedAndCumulative(t *testing.T) {
	usage, err := aiPolicyReadUsage("openai", []byte(`{"usage":{"prompt_tokens":120,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":12}}}`), aiUsage{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Known || !usage.InputKnown || !usage.OutputKnown || usage.InputTokens != 120 || usage.OutputTokens != 9 || usage.CacheReadTokens != 12 {
		t.Fatalf("unexpected OpenAI usage: %+v", usage)
	}
	usage, err = aiPolicyReadUsage("openai", []byte(`{"usage":{"prompt_tokens":120,"completion_tokens":15}}`), usage, true)
	if err != nil || usage.OutputTokens != 15 {
		t.Fatalf("cumulative usage was added instead of replaced: %+v, %v", usage, err)
	}
}

func TestAIPolicyUsageAnthropicNestedInputAndFinalOutput(t *testing.T) {
	usage, err := aiPolicyReadUsage("anthropic", []byte(`{"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":4,"cache_read_input_tokens":6}}}`), aiUsage{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Known || !usage.InputKnown || usage.InputTokens != 110 || usage.CacheWriteTokens != 4 || usage.CacheReadTokens != 6 {
		t.Fatalf("unexpected Anthropic input usage: %+v", usage)
	}
	usage, err = aiPolicyReadUsage("anthropic", []byte(`{"type":"message_delta","usage":{"output_tokens":17}}`), usage, true)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Known || !usage.OutputKnown || usage.OutputTokens != 17 || usage.InputTokens != 110 {
		t.Fatalf("unexpected Anthropic final usage: %+v", usage)
	}
}

func TestAIPolicyUsageMissingAnthropicInputStaysUnknown(t *testing.T) {
	usage, err := aiPolicyReadUsage("anthropic", []byte(`{"type":"message_start","message":{"usage":{"cache_read_input_tokens":6}}}`), aiUsage{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Known || usage.InputKnown || usage.InputTokens != 0 || usage.CacheReadTokens != 6 {
		t.Fatalf("missing input was treated as known: %+v", usage)
	}
}

func TestAIPolicyUsageAnthropicCacheOnlyUpdatePreservesBaseInput(t *testing.T) {
	prior := aiUsage{InputTokens: 110, CacheReadTokens: 6, CacheWriteTokens: 4, InputKnown: true}
	usage, err := aiPolicyReadUsage("anthropic", []byte(`{"usage":{"cache_read_input_tokens":12,"output_tokens":3}}`), prior, true)
	if err != nil || usage.InputTokens != 116 || usage.CacheReadTokens != 12 || !usage.Known {
		t.Fatalf("cache update lost base input: %+v %v", usage, err)
	}
}

func TestAIPolicyUsageRejectsNonIntegerAndOversizedCounters(t *testing.T) {
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":1.5,"completion_tokens":1}}`,
		`{"usage":{"prompt_tokens":-1,"completion_tokens":1}}`,
		`{"usage":{"prompt_tokens":10000001,"completion_tokens":1}}`,
	} {
		if _, err := aiPolicyReadUsage("openai", []byte(raw), aiUsage{}, true); err == nil {
			t.Fatalf("accepted malformed usage %s", raw)
		}
	}
}
