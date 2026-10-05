//go:build qg_ai_streaming && qg_openai

package modules

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/mgg789/QGramm/internal/config"
)

func init() { aiOpenAIStreamFn = aiOpenAIStream }

func aiOpenAIStream(ctx context.Context, c config.Config, turns []aiTurn, tools []config.Tool, callback aiStreamCallback) (aiAnswer, error) {
	headers, headerErr := aiProviderHeaders(c.AI, "openai")
	if headerErr != nil {
		return aiAnswer{}, headerErr
	}
	messages := make([]map[string]any, 0, len(turns))
	if c.AI.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": c.AI.SystemPrompt})
	}
	for _, t := range turns {
		m := map[string]any{"role": t.Role, "content": t.Content}
		if t.ToolCallID != "" {
			m["tool_call_id"] = t.ToolCallID
		}
		if len(t.Calls) > 0 {
			calls := make([]map[string]any, 0, len(t.Calls))
			for _, call := range t.Calls {
				calls = append(calls, map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(call.Arguments)}})
			}
			m["tool_calls"] = calls
		}
		messages = append(messages, m)
	}
	body := map[string]any{"model": c.AI.Model, "messages": messages, "max_tokens": aiMaxOutputTokens(c.AI), "stream": true}
	if len(tools) > 0 {
		defs := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "additionalProperties": false}
			}
			defs = append(defs, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "parameters": schema}})
		}
		body["tools"] = defs
	}
	headers["Accept"] = "text/event-stream"
	stream, err := aiRequestStream(ctx, strings.TrimRight(c.AI.OpenAIURL, "/")+"/chat/completions", headers, body, c.AI.AllowPrivate)
	if err != nil {
		return aiAnswer{}, err
	}
	defer stream.Close()

	type callState struct {
		id, name string
		args     strings.Builder
		started  bool
		pending  strings.Builder
	}
	calls := map[int]*callState{}
	callIDs := map[string]bool{}
	var answer aiAnswer
	terminated := false
	finished := false
	err = aiReadSSE(ctx, stream, aiContextLimits(ctx).response, func(event aiSSEEvent) (bool, error) {
		if strings.TrimSpace(event.Data) == "[DONE]" {
			terminated = true
			return true, nil
		}
		var wire struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
				Delta        struct {
					Content   *string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := aiJSONEvent(event.Data, &wire); err != nil {
			return false, err
		}
		if len(wire.Error) > 0 && string(wire.Error) != "null" {
			return false, errors.New("provider stream error")
		}
		if len(wire.Choices) == 0 {
			return false, nil // provider usage/metadata event
		}
		delta := wire.Choices[0].Delta
		if wire.Choices[0].FinishReason != nil {
			switch *wire.Choices[0].FinishReason {
			case "stop", "tool_calls":
				finished = true
			default:
				return false, errors.New("provider stream terminated unsuccessfully")
			}
		}
		if delta.Content != nil && *delta.Content != "" {
			if err := aiStreamEmit(callback, "text.delta", map[string]string{"text": *delta.Content}); err != nil {
				return false, err
			}
			answer.Text += *delta.Content
		}
		for _, tc := range delta.ToolCalls {
			if tc.Index < 0 || tc.Index > 7 {
				return false, errors.New("invalid provider tool call index")
			}
			state := calls[tc.Index]
			if state == nil {
				state = &callState{}
				calls[tc.Index] = state
			}
			if tc.ID != "" {
				if state.id != "" && state.id != tc.ID {
					return false, errors.New("provider tool call id changed")
				}
				if callIDs[tc.ID] && state.id != tc.ID {
					return false, errors.New("duplicate provider tool call id")
				}
				state.id = tc.ID
				callIDs[tc.ID] = true
			}
			if tc.Function.Name != "" {
				if state.name != "" && state.name != tc.Function.Name {
					return false, errors.New("provider tool call name changed")
				}
				state.name = tc.Function.Name
			}
			if !state.started && state.id != "" && state.name != "" {
				if err := aiStreamEmit(callback, "tool.started", map[string]any{"index": tc.Index, "id": state.id, "name": state.name}); err != nil {
					return false, err
				}
				state.started = true
				if state.pending.Len() > 0 {
					pending := state.pending.String()
					if err := aiStreamEmit(callback, "tool.arguments.delta", map[string]any{"index": tc.Index, "arguments": pending}); err != nil {
						return false, err
					}
					state.args.WriteString(pending)
					state.pending.Reset()
				}
			}
			if tc.Function.Arguments != "" {
				if state.args.Len()+state.pending.Len()+len(tc.Function.Arguments) > 65536 {
					return false, errors.New("tool arguments too large")
				}
				if state.started {
					if err := aiStreamEmit(callback, "tool.arguments.delta", map[string]any{"index": tc.Index, "arguments": tc.Function.Arguments}); err != nil {
						return false, err
					}
					state.args.WriteString(tc.Function.Arguments)
				} else {
					state.pending.WriteString(tc.Function.Arguments)
				}
			}
		}
		return false, nil
	})
	if err != nil {
		return aiAnswer{}, err
	}
	if !terminated || !finished {
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
		if index != len(answer.Calls) {
			return aiAnswer{}, errors.New("non-contiguous provider tool call indices")
		}
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
