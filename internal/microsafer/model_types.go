//go:build qg_ai_endpoint && qg_e2ee

package microsafer

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatCompletionRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

type ExternalPlaintextNotice struct {
	Kind   string `json:"kind"`
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

type ModelResponseFunc func(RPCResponse) error
