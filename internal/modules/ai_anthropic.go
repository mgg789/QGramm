//go:build qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func init() { aiProviders["anthropic"] = aiAnthropic; core.Register("anthropic", installAI) }
func aiAnthropic(ctx context.Context, c config.Config, turns []aiTurn, tools []config.Tool, request aiRequester) (aiAnswer, error) {
	if c.Features.AIPolicy {
		if err := aiAssertPolicyEffect(ctx, true); err != nil {
			return aiAnswer{}, err
		}
	}
	if callback := aiStreamCallbackFromContext(ctx); callback != nil {
		if aiAnthropicStreamFn == nil {
			return aiAnswer{}, errors.New("provider streaming unavailable")
		}
		return aiAnthropicStreamFn(ctx, c, turns, tools, callback)
	}
	headers, headerErr := aiProviderHeaders(c.AI, "anthropic")
	if headerErr != nil {
		return aiAnswer{}, headerErr
	}
	messages := []map[string]any{}
	for _, t := range turns {
		if t.Role == "system" {
			continue
		}
		role := t.Role
		blocks := []map[string]any{}
		if role == "tool" {
			role = "user"
			blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": t.ToolCallID, "content": t.Content})
		} else {
			if t.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": t.Content})
			}
			for _, call := range t.Calls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Arguments})
			}
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	body := map[string]any{"model": c.AI.Model, "messages": messages, "max_tokens": aiMaxOutputTokens(c.AI), "stream": false}
	if c.AI.SystemPrompt != "" {
		body["system"] = c.AI.SystemPrompt
	}
	if len(tools) > 0 {
		defs := []map[string]any{}
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "additionalProperties": false}
			}
			defs = append(defs, map[string]any{"name": t.Name, "input_schema": schema})
		}
		body["tools"] = defs
	}
	data, e := request(ctx, strings.TrimRight(c.AI.AnthropicURL, "/")+"/messages", headers, body, c.AI.AllowPrivate)
	if e != nil {
		return aiAnswer{}, e
	}
	var response struct {
		Usage   json.RawMessage `json:"usage"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if json.Unmarshal(data, &response) != nil {
		return aiAnswer{}, errors.New("invalid provider response")
	}
	var usage aiUsage
	if c.Features.AIPolicy && aiReadUsage != nil {
		var err error
		usage, err = aiReadUsage("anthropic", data, usage, true)
		if err != nil {
			return aiAnswer{}, err
		}
	}
	a := aiAnswer{Usage: usage}
	for _, block := range response.Content {
		switch block.Type {
		case "text":
			a.Text += block.Text
		case "tool_use":
			if block.ID == "" || !json.Valid(block.Input) {
				return a, errors.New("invalid provider tool call")
			}
			a.Calls = append(a.Calls, aiCall{ID: block.ID, Name: block.Name, Arguments: block.Input})
		}
	}
	if a.Text == "" && len(a.Calls) == 0 {
		return a, errors.New("empty provider response")
	}
	return a, nil
}
