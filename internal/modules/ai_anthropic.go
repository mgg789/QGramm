//go:build qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

func init() { aiProviders["anthropic"] = aiAnthropic; core.Register("anthropic", installAI) }
func aiAnthropic(ctx context.Context, c config.Config, turns []aiTurn, tools []config.Tool, request aiRequester) (aiAnswer, error) {
	key := os.Getenv(c.AI.AnthropicKeyEnv)
	if key == "" {
		return aiAnswer{}, errors.New("provider credential unavailable")
	}
	messages := []map[string]any{}
	for _, t := range turns {
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
	body := map[string]any{"model": c.AI.Model, "messages": messages, "max_tokens": 2048, "stream": false}
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
	data, e := request(ctx, strings.TrimRight(c.AI.AnthropicURL, "/")+"/messages", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, body, false)
	if e != nil {
		return aiAnswer{}, e
	}
	var response struct {
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
	a := aiAnswer{}
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
