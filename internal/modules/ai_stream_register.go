//go:build qg_ai_streaming && (qg_openai || qg_anthropic)

package modules

import "github.com/mgg789/QGramm/internal/core"

// Streaming shares the encrypted AI job installation; the optional progress
// installer adds its encrypted progress storage and routes when configured.
func init() {
	aiStreamContext = aiStreamWithCallback
	core.Register("ai_streaming", installAI)
}
