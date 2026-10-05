//go:build qg_ai_streaming && qg_anthropic

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
)

func init() { aiAnthropicStreamFn = aiAnthropicStream }

func aiAnthropicStream(ctx context.Context, c config.Config, turns []aiTurn, tools []config.Tool, callback aiStreamCallback) (aiAnswer, error) {
	headers, headerErr := aiProviderHeaders(c.AI, "anthropic")
	if headerErr != nil {
		return aiAnswer{}, headerErr
	}
	messages := make([]map[string]any, 0, len(turns))
	for _, t := range turns {
		if t.Role == "system" {
			continue
		}
		role := t.Role
		blocks := make([]map[string]any, 0, 1+len(t.Calls))
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
	body := map[string]any{"model": c.AI.Model, "messages": messages, "max_tokens": aiMaxOutputTokens(c.AI), "stream": true}
	if c.AI.SystemPrompt != "" {
		body["system"] = c.AI.SystemPrompt
	}
	if len(tools) > 0 {
		defs := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "additionalProperties": false}
			}
			defs = append(defs, map[string]any{"name": t.Name, "input_schema": schema})
		}
		body["tools"] = defs
	}
	headers["Accept"] = "text/event-stream"
	stream, err := aiRequestStream(ctx, strings.TrimRight(c.AI.AnthropicURL, "/")+"/messages", headers, body, c.AI.AllowPrivate)
	if err != nil {
		return aiAnswer{}, err
	}
	defer stream.Close()
	type callState struct {
		id, name string
		args     strings.Builder
		started  bool
	}
	calls := map[int]*callState{}
	callIDs := map[string]bool{}
	var answer aiAnswer
	terminated := false
	finished := false
	err = aiReadSSE(ctx, stream, aiContextLimits(ctx).response, func(event aiSSEEvent) (bool, error) {
		var header struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string  `json:"type"`
				Text        string  `json:"text"`
				PartialJSON string  `json:"partial_json"`
				StopReason  *string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := aiJSONEvent(event.Data, &header); err != nil {
			return false, err
		}
		switch header.Type {
		case "content_block_start":
			if header.Index < 0 || header.ContentBlock.Type != "tool_use" || header.ContentBlock.ID == "" || header.ContentBlock.Name == "" {
				if header.ContentBlock.Type == "text" {
					return false, nil
				}
				return false, errors.New("invalid provider tool call")
			}
			if _, exists := calls[header.Index]; exists {
				return false, errors.New("duplicate provider tool call index")
			}
			if callIDs[header.ContentBlock.ID] {
				return false, errors.New("duplicate provider tool call id")
			}
			callIDs[header.ContentBlock.ID] = true
			calls[header.Index] = &callState{id: header.ContentBlock.ID, name: header.ContentBlock.Name, started: true}
			if err := aiStreamEmit(callback, "tool.started", map[string]any{"index": header.Index, "id": header.ContentBlock.ID, "name": header.ContentBlock.Name}); err != nil {
				return false, err
			}
		case "content_block_delta":
			switch header.Delta.Type {
			case "text_delta":
				if header.Delta.Text != "" {
					if err := aiStreamEmit(callback, "text.delta", map[string]string{"text": header.Delta.Text}); err != nil {
						return false, err
					}
					answer.Text += header.Delta.Text
				}
			case "input_json_delta":
				state := calls[header.Index]
				if state == nil || !state.started || header.Delta.PartialJSON == "" {
					return false, errors.New("tool arguments arrived before tool start")
				}
				if state.args.Len()+len(header.Delta.PartialJSON) > 65536 {
					return false, errors.New("tool arguments too large")
				}
				if err := aiStreamEmit(callback, "tool.arguments.delta", map[string]any{"index": header.Index, "arguments": header.Delta.PartialJSON}); err != nil {
					return false, err
				}
				state.args.WriteString(header.Delta.PartialJSON)
			}
		case "message_stop":
			if !finished {
				return false, errors.New("provider stream stop reason missing")
			}
			terminated = true
			return true, nil
		case "message_start", "content_block_stop", "message_delta", "ping":
			// Metadata and block boundaries carry no user-visible delta.
			if header.Type == "message_delta" {
				if header.Delta.StopReason == nil {
					return false, errors.New("provider stream stop reason missing")
				}
				switch *header.Delta.StopReason {
				case "end_turn", "tool_use":
					finished = true
				default:
					return false, errors.New("provider stream terminated unsuccessfully")
				}
			}
		default:
			return false, errors.New("unknown provider stream event")
		}
		return false, nil
	})
	if err != nil {
		return aiAnswer{}, err
	}
	if !terminated {
		return aiAnswer{}, errors.New("provider stream termination missing")
	}
	if len(calls) > 8 {
		return aiAnswer{}, errors.New("too many tool calls")
	}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		state := calls[index]
		if state == nil || !state.started || state.id == "" || state.name == "" {
			return aiAnswer{}, errors.New("invalid provider tool call")
		}
		args := state.args.String()
		if args == "" {
			args = "{}"
		}
		if !json.Valid([]byte(args)) {
			return aiAnswer{}, errors.New("invalid provider tool call")
		}
		answer.Calls = append(answer.Calls, aiCall{ID: state.id, Name: state.name, Arguments: json.RawMessage(args)})
	}
	if answer.Text == "" && len(answer.Calls) == 0 {
		return aiAnswer{}, errors.New("empty provider response")
	}
	return answer, nil
}
