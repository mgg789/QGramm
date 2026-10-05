//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const aiPolicyMaxUsageCounter int64 = 10_000_000

func init() { aiReadUsage = aiPolicyReadUsage }

// aiPolicyReadUsage parses one provider response or SSE event. Provider stream
// usage events carry cumulative counters, so every field replaces the prior
// snapshot rather than being added to it. The final argument is controlled by
// the adapter: it marks an event at which a complete provider usage result is
// allowed to become Known.
func aiPolicyReadUsage(provider string, data []byte, prior aiUsage, final bool) (aiUsage, error) {
	var envelope map[string]json.RawMessage
	if err := decodeUsageObject(data, &envelope); err != nil {
		return prior, err
	}
	if envelope == nil {
		return prior, nil
	}
	raw, ok := envelope["usage"]
	if provider == "anthropic" && !ok {
		// Anthropic puts prompt/cache counters in message_start.message.usage,
		// while message_delta.usage carries the cumulative output counter.
		if messageRaw, hasMessage := envelope["message"]; hasMessage && !isJSONNull(messageRaw) {
			var message map[string]json.RawMessage
			if err := decodeUsageObject(messageRaw, &message); err != nil || message == nil {
				return prior, errors.New("invalid provider usage message")
			}
			raw, ok = message["usage"]
		}
	}
	if !ok || isJSONNull(raw) {
		return prior, nil
	}
	var usage map[string]json.RawMessage
	if err := decodeUsageObject(raw, &usage); err != nil {
		return prior, errors.New("invalid provider usage")
	}
	if usage == nil {
		return prior, errors.New("invalid provider usage")
	}
	switch provider {
	case "openai":
		return aiPolicyReadOpenAIUsage(usage, prior, final)
	case "anthropic":
		return aiPolicyReadAnthropicUsage(usage, prior, final)
	default:
		return prior, errors.New("unknown provider usage")
	}
}

func decodeUsageObject(data []byte, out *map[string]json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid provider usage event")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid provider usage event")
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func aiPolicyCounter(fields map[string]json.RawMessage, name string) (int64, bool, error) {
	raw, ok := fields[name]
	if !ok {
		return 0, false, nil
	}
	value, err := aiPolicyStrictCounter(raw)
	if err != nil {
		return 0, false, fmt.Errorf("invalid provider usage %s", name)
	}
	return value, true, nil
}

func aiPolicyStrictCounter(raw json.RawMessage) (int64, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return 0, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return 0, errors.New("trailing value")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("counter is not a number")
	}
	text := string(number)
	if text == "" || strings.HasPrefix(text, "-") || strings.ContainsAny(text, ".eE") {
		return 0, errors.New("counter is not a non-negative integer")
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed < 0 || parsed > aiPolicyMaxUsageCounter {
		return 0, errors.New("counter out of range")
	}
	return parsed, nil
}

func aiPolicyReadOpenAIUsage(fields map[string]json.RawMessage, prior aiUsage, final bool) (aiUsage, error) {
	input, inputSeen, err := aiPolicyFirstCounter(fields, "prompt_tokens", "input_tokens")
	if err != nil {
		return prior, err
	}
	output, outputSeen, err := aiPolicyFirstCounter(fields, "completion_tokens", "output_tokens")
	if err != nil {
		return prior, err
	}
	read, readSeen, err := aiPolicyCounter(fields, "cache_read_input_tokens")
	if err != nil {
		return prior, err
	}
	write, writeSeen, err := aiPolicyCounter(fields, "cache_creation_input_tokens")
	if err != nil {
		return prior, err
	}
	if detailsRaw, ok := fields["prompt_tokens_details"]; ok {
		var details map[string]json.RawMessage
		if err := decodeUsageObject(detailsRaw, &details); err != nil || details == nil {
			return prior, errors.New("invalid provider usage prompt details")
		}
		if cached, seen, err := aiPolicyCounter(details, "cached_tokens"); err != nil {
			return prior, err
		} else if seen {
			read, readSeen = cached, true
		}
		if created, seen, err := aiPolicyCounter(details, "cache_creation_input_tokens"); err != nil {
			return prior, err
		} else if seen {
			write, writeSeen = created, true
		}
	}
	if inputSeen {
		prior.InputTokens = input
		prior.InputKnown = true
	}
	if outputSeen {
		prior.OutputTokens = output
		prior.OutputKnown = true
	}
	if readSeen {
		prior.CacheReadTokens = read
	}
	if writeSeen {
		prior.CacheWriteTokens = write
	}
	if final && prior.InputKnown && prior.OutputKnown {
		prior.Known = true
	}
	return prior, nil
}

func aiPolicyReadAnthropicUsage(fields map[string]json.RawMessage, prior aiUsage, final bool) (aiUsage, error) {
	previousCacheRead, previousCacheWrite := prior.CacheReadTokens, prior.CacheWriteTokens
	base, inputSeen, err := aiPolicyCounter(fields, "input_tokens")
	if err != nil {
		return prior, err
	}
	output, outputSeen, err := aiPolicyCounter(fields, "output_tokens")
	if err != nil {
		return prior, err
	}
	read, readSeen, err := aiPolicyCounter(fields, "cache_read_input_tokens")
	if err != nil {
		return prior, err
	}
	write, writeSeen, err := aiPolicyCounter(fields, "cache_creation_input_tokens")
	if err != nil {
		return prior, err
	}
	if inputSeen {
		prior.InputKnown = true
	}
	if readSeen {
		prior.CacheReadTokens = read
	}
	if writeSeen {
		prior.CacheWriteTokens = write
	}
	if inputSeen || (prior.InputKnown && (readSeen || writeSeen)) {
		// Anthropic reports input_tokens and cache counters separately. Keep the
		// normalized total while retaining each cache component for accounting.
		if !inputSeen {
			base = prior.InputTokens - previousCacheRead - previousCacheWrite
			if base < 0 {
				return prior, errors.New("invalid provider usage input total")
			}
		}
		total, ok := aiPolicyUsageAdd(base, prior.CacheReadTokens, prior.CacheWriteTokens)
		if !ok {
			return prior, errors.New("provider usage input total out of range")
		}
		prior.InputTokens = total
	}
	if outputSeen && final {
		prior.OutputTokens = output
		prior.OutputKnown = true
	}
	if final && prior.InputKnown && prior.OutputKnown {
		prior.Known = true
	}
	return prior, nil
}

func aiPolicyFirstCounter(fields map[string]json.RawMessage, names ...string) (int64, bool, error) {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return aiPolicyCounter(fields, name)
		}
	}
	return 0, false, nil
}

func aiPolicyUsageAdd(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || value > aiPolicyMaxUsageCounter-total {
			return 0, false
		}
		total += value
	}
	return total, true
}
