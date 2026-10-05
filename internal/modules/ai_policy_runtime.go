//go:build qg_ai_policy && (qg_openai || qg_anthropic)

package modules

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mgg789/QGramm/internal/config"
	"github.com/mgg789/QGramm/internal/core"
)

type aiPolicyTicketKey struct{}
type aiContinuation struct {
	Version        int      `json:"version"`
	Chat           string   `json:"chat_id"`
	SourceMessage  string   `json:"source_message"`
	SourceUser     string   `json:"source_user"`
	SourceDevice   string   `json:"source_device"`
	AIUser         string   `json:"ai_user"`
	AIDevice       string   `json:"ai_device"`
	Epoch          int64    `json:"epoch"`
	SessionVersion int64    `json:"session_version"`
	Turns          []aiTurn `json:"turns"`
	Step           int      `json:"step"`
	NextCall       int      `json:"next_call"`
	Calls          []aiCall `json:"calls"`
	ProviderNeeded bool     `json:"provider_needed"`
}

func init() {
	core.Register("ai_policy", installAI)
	aiPolicyInstall = installAIPolicy
	aiSavedContinuation = aiPolicySaved
	aiPolicyConversation = runAIPolicyConversation
	aiPolicyActive = func(ctx context.Context) bool { id, _ := ctx.Value(aiPolicyTicketKey{}).(string); return id != "" }
}

func policyIdentity(ctx context.Context, c *core.Core, job string) (aiTaskIdentity, int64, error) {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return aiTaskIdentity{}, 0, err
	}
	defer tx.Rollback()
	identity, err := aiTaskLookup(ctx, tx, job)
	if err != nil || identity.Status != "running" || !aiTaskAuthorized(ctx, tx, identity) {
		return identity, 0, errors.New("AI policy task access revoked")
	}
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT s.version FROM ai_sessions s JOIN ai_tasks t ON t.chat_id=s.chat_id AND t.agent_id=s.agent_id WHERE t.id=?`, job).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return identity, version, err
}

func runAIPolicyConversation(ctx context.Context, c *core.Core, job string, fn aiProvider, turns []aiTurn, tools []config.Tool, request aiRequester) (string, error) {
	if fn == nil {
		return "", errors.New("AI provider unavailable")
	}
	settings := aiSettings(ctx, c)
	cfg := aiEffectiveConfig(ctx, c)
	request = aiBoundRequester(request)
	identity, version, err := policyIdentity(ctx, c, job)
	if err != nil {
		return "", err
	}
	state := aiContinuation{Version: 1, Chat: identity.Chat, SourceMessage: identity.SourceMessage, SourceUser: identity.SourceUser, SourceDevice: identity.SourceDevice, AIUser: identity.User, AIDevice: identity.Device, Epoch: identity.Epoch, SessionVersion: version, Turns: aiBoundContext(turns, settings), ProviderNeeded: true}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	saved, err := aiPolicySaved(ctx, tx, c, job)
	_ = tx.Rollback()
	if err != nil {
		return "", err
	}
	if len(saved) > 0 {
		if len(saved) > 1048576 {
			return "", errors.New("AI continuation too large")
		}
		dec := json.NewDecoder(strings.NewReader(string(saved)))
		dec.DisallowUnknownFields()
		if dec.Decode(&state) != nil || state.Version != 1 || state.Chat != identity.Chat || state.SourceMessage != identity.SourceMessage || state.SourceUser != identity.SourceUser || state.SourceDevice != identity.SourceDevice || state.AIUser != identity.User || state.AIDevice != identity.Device || state.Epoch != identity.Epoch || state.SessionVersion != version || state.Step < 0 || state.Step >= settings.MaxSteps || state.NextCall < 0 || state.NextCall > len(state.Calls) || len(state.Calls) > 8 {
			return "", errors.New("AI continuation invalidated")
		}
	}
	for state.Step < settings.MaxSteps {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		if !aiEffectsAllowed(ctx, c, job) {
			return "", errors.New("AI task access revoked")
		}
		raw, _ := json.Marshal(state.Turns)
		if len(raw) > settings.MaxContextBytes || len(state.Turns) > settings.MaxContextTurns {
			return "", errors.New("AI context exhausted")
		}
		if state.ProviderNeeded {
			providerName, destination := policyProvider(ctx, c, job, settings)
			if providerName == "" {
				return "", errors.New("AI provider identity unavailable")
			}
			fingerprint := struct {
				Version                 int
				Provider, Model, Prompt string
				MaxOutputTokens         int
				Turns                   []aiTurn
				Tools                   []config.Tool
			}{1, providerName, settings.Model, settings.SystemPrompt, aiMaxOutputTokens(settings), state.Turns, tools}
			effect := makePolicyEffect(job, state.Step, -1, "provider", providerName, destination, fingerprint, c.Config.AIPolicy.RequireProviderApproval, c.Config.AIPolicy.ProviderReserveMicrounits, settings, state)
			payload, _ := json.Marshal(state)
			if err = flushPolicyProgress(ctx); err != nil {
				return "", err
			}
			effectID, beginErr := aiPolicyBegin(ctx, c, effect, payload)
			if beginErr != nil {
				return "", beginErr
			}
			effectCtx := context.WithValue(ctx, aiPolicyTicketKey{}, effectID)
			answer, callErr := fn(effectCtx, cfg, state.Turns, tools, request)
			outcome := "succeeded"
			if callErr != nil {
				outcome = "uncertain"
			}
			if settleErr := settlePolicyEffect(ctx, c, effectID, answer.Usage, outcome); settleErr != nil {
				return "", settleErr
			}
			if callErr != nil {
				return "", callErr
			}
			if len(answer.Calls) == 0 {
				return answer.Text, nil
			}
			if len(answer.Calls) > 8 {
				return "", errors.New("too many AI tool calls")
			}
			state.Turns = append(state.Turns, aiTurn{Role: "assistant", Content: answer.Text, Calls: answer.Calls})
			state.Calls = answer.Calls
			state.NextCall = 0
			state.ProviderNeeded = false
		}
		for state.NextCall < len(state.Calls) {
			call := state.Calls[state.NextCall]
			tool, toolErr := aiAuthorizedTool(ctx, c, job, call, tools)
			if toolErr != nil {
				return "", toolErr
			}
			canonical, canonErr := aiCanonicalArguments(call.Arguments)
			if canonErr != nil {
				return "", canonErr
			}
			call.Arguments = canonical
			state.Calls[state.NextCall] = call
			// Rebind the assistant turn to exactly the arguments forwarded to the tool.
			for i := len(state.Turns) - 1; i >= 0; i-- {
				if state.Turns[i].Role == "assistant" && len(state.Turns[i].Calls) > 0 {
					state.Turns[i].Calls = state.Calls
					break
				}
			}
			fingerprint := struct {
				Version   int
				Tool      config.Tool
				Arguments json.RawMessage
			}{1, tool, canonical}
			effect := makePolicyEffect(job, state.Step, state.NextCall, "tool", tool.Name, tool.URL, fingerprint, tool.RequireApproval, tool.CostMicrounits, settings, state)
			payload, _ := json.Marshal(state)
			if err = flushPolicyProgress(ctx); err != nil {
				return "", err
			}
			effectID, beginErr := aiPolicyBegin(ctx, c, effect, payload)
			if beginErr != nil {
				return "", beginErr
			}
			effectCtx := context.WithValue(ctx, aiPolicyTicketKey{}, effectID)
			result, callErr := aiExecuteTool(effectCtx, c, job, tool, call, request)
			outcome := "succeeded"
			if callErr != nil {
				outcome = "uncertain"
			}
			if settleErr := settlePolicyEffect(ctx, c, effectID, aiUsage{}, outcome); settleErr != nil {
				return "", settleErr
			}
			if callErr != nil {
				return "", callErr
			}
			state.Turns = append(state.Turns, aiTurn{Role: "tool", Content: result, ToolCallID: call.ID})
			state.NextCall++
		}
		state.Step++
		state.ProviderNeeded = true
		state.Calls = nil
		state.NextCall = 0
	}
	return "", errors.New("AI step limit reached")
}

func flushPolicyProgress(ctx context.Context) error {
	if flush, ok := ctx.Value(aiProgressFlushKey{}).(func() error); ok {
		return flush()
	}
	return nil
}

func policyProvider(ctx context.Context, c *core.Core, job string, settings config.AI) (string, string) {
	var provider string
	if c.DB.QueryRowContext(ctx, `SELECT a.provider FROM ai_chats a JOIN ai_jobs j ON j.chat_id=a.chat_id WHERE j.id=? UNION ALL SELECT a.provider FROM ai_agents a JOIN ai_tasks t ON t.agent_id=a.id WHERE t.id=?`, job, job).Scan(&provider) != nil {
		return "", ""
	}
	// Current bot settings may switch provider after restart; follow validated TOML.
	var bot string
	if c.DB.QueryRowContext(ctx, `SELECT a.bot_name FROM ai_agents a JOIN ai_tasks t ON t.agent_id=a.id WHERE t.id=?`, job).Scan(&bot) == nil {
		_, current, err := c.Config.ResolveBot(bot)
		if err != nil {
			return "", ""
		}
		provider = current
	}
	if provider == "openai" {
		return provider, strings.TrimRight(settings.OpenAIURL, "/") + "/chat/completions"
	}
	if provider == "anthropic" {
		return provider, strings.TrimRight(settings.AnthropicURL, "/") + "/messages"
	}
	return "", ""
}

func makePolicyEffect(job string, step, index int, action, name, destination string, request any, required bool, reserve int64, settings config.AI, state aiContinuation) aiPolicyEffect {
	raw, _ := json.Marshal(struct {
		Version                          int
		Epoch, SessionVersion            int64
		Request                          any
		Destination                      string
		Reserve, InputPrice, OutputPrice int64
	}{1, state.Epoch, state.SessionVersion, request, destination, reserve, settings.InputPriceMicrounitsPerMillionTokens, settings.OutputPriceMicrounitsPerMillionTokens})
	hash := sha256.Sum256(raw)
	id := sha256.Sum256([]byte(fmt.Sprintf("qgramm-ai-effect-v1/%s/%s/%d/%d", job, action, step, index)))
	return aiPolicyEffect{ID: hex.EncodeToString(id[:]), Job: job, Action: action, Name: name, Destination: destination, RequestHash: hex.EncodeToString(hash[:]), Required: required, Epoch: state.Epoch, SessionVersion: state.SessionVersion, ReserveMicrounits: reserve, InputPriceMicrounitsPerMillionTokens: settings.InputPriceMicrounitsPerMillionTokens, OutputPriceMicrounitsPerMillionTokens: settings.OutputPriceMicrounitsPerMillionTokens}
}

// Version 1 canonicalization sorts object keys, preserves JSON number spelling,
// rejects duplicate keys and limits nesting. It is deliberately not RFC 8785.
func aiCanonicalArguments(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	value, err := policyJSONValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing tool JSON")
	}
	encoded, err := json.Marshal(value)
	return encoded, err
}
func policyJSONValue(dec *json.Decoder, depth int) (any, error) {
	if depth > 8 {
		return nil, errors.New("tool JSON nesting limit")
	}
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delim {
	case '{':
		value := map[string]any{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("invalid JSON key")
			}
			if _, exists := value[name]; exists {
				return nil, errors.New("duplicate tool JSON key")
			}
			child, err := policyJSONValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			value[name] = child
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return value, nil
	case '[':
		value := []any{}
		for dec.More() {
			child, err := policyJSONValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			value = append(value, child)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, errors.New("invalid JSON delimiter")
	}
}

func settlePolicyEffect(ctx context.Context, c *core.Core, id string, usage aiUsage, outcome string) error {
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return aiPolicySettle(bounded, c, id, usage, outcome)
}
