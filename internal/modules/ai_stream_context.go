//go:build qg_openai || qg_anthropic

package modules

import (
	"context"
	"encoding/json"

	"github.com/mgg789/QGramm/internal/config"
)

// aiStreamCallback receives provider deltas. The callback is deliberately
// carried by context: a request is streamed only when the caller installed
// one, so existing callers and test requesters keep the buffered contract.
// Payloads are provider-neutral JSON objects. The current event kinds are
// text.delta, tool.started, and tool.arguments.delta.
type aiStreamCallback func(kind string, payload json.RawMessage) error

type aiStreamContextKey struct{}

// aiStreamWithCallback is the implementation used by aiStreamContext in
// ai.go. Keeping the key here means the optional streaming module does not
// require a configuration field or a second provider API.
func aiStreamWithCallback(ctx context.Context, callback func(string, json.RawMessage) error) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, aiStreamContextKey{}, aiStreamCallback(callback))
}

func aiStreamCallbackFromContext(ctx context.Context) aiStreamCallback {
	if callback, ok := ctx.Value(aiStreamContextKey{}).(aiStreamCallback); ok {
		return callback
	}
	return nil
}

// The provider adapters are compiled in the normal qg_openai/qg_anthropic
// profiles too. The optional module fills these hooks when qg_ai_streaming is
// selected; this avoids changing the aiRequester compatibility contract.
var aiOpenAIStreamFn func(context.Context, config.Config, []aiTurn, []config.Tool, aiStreamCallback) (aiAnswer, error)
var aiAnthropicStreamFn func(context.Context, config.Config, []aiTurn, []config.Tool, aiStreamCallback) (aiAnswer, error)

func aiMaxOutputTokens(c config.AI) int {
	if c.MaxOutputTokens > 0 {
		return c.MaxOutputTokens
	}
	return 2048
}
