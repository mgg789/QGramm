//go:build qg_openai

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func init() { aiProviders["openai"] = aiOpenAI; core.Register("openai", installAI) }
func aiOpenAI(ctx context.Context, c config.Config, turns []aiTurn, tools []config.Tool, request aiRequester) (aiAnswer, error) {
	if callback := aiStreamCallbackFromContext(ctx); callback != nil {
		if aiOpenAIStreamFn == nil {
			return aiAnswer{}, errors.New("provider streaming unavailable")
		}
		return aiOpenAIStreamFn(ctx, c, turns, tools, callback)
	}
	headers, headerErr := aiProviderHeaders(c.AI, "openai")
	if headerErr != nil {
		return aiAnswer{}, headerErr
	}
	messages := []map[string]any{}
	if c.AI.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": c.AI.SystemPrompt})
	}
	for _, t := range turns {
		m := map[string]any{"role": t.Role, "content": t.Content}
		if t.ToolCallID != "" {
			m["tool_call_id"] = t.ToolCallID
		}
		if len(t.Calls) > 0 {
			calls := []map[string]any{}
			for _, call := range t.Calls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)}})
			}
			m["tool_calls"] = calls
		}
		messages = append(messages, m)
	}
	body := map[string]any{"model": c.AI.Model, "messages": messages, "max_tokens": aiMaxOutputTokens(c.AI), "stream": false}
	if len(tools) > 0 {
		defs := []map[string]any{}
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "additionalProperties": false}
			}
			defs = append(defs, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "parameters": schema}})
		}
		body["tools"] = defs
	}
	data, e := request(ctx, strings.TrimRight(c.AI.OpenAIURL, "/")+"/chat/completions", headers, body, c.AI.AllowPrivate)
	if e != nil {
		return aiAnswer{}, e
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Choices) != 1 {
		return aiAnswer{}, errors.New("invalid provider response")
	}
	m := response.Choices[0].Message
	a := aiAnswer{Text: m.Content}
	for _, call := range m.ToolCalls {
		if call.ID == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return aiAnswer{}, errors.New("invalid provider tool call")
		}
		a.Calls = append(a.Calls, aiCall{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)})
	}
	if a.Text == "" && len(a.Calls) == 0 {
		return a, errors.New("empty provider response")
	}
	return a, nil
}
