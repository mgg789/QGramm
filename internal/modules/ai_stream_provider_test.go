//go:build qg_ai_streaming && (qg_openai || qg_anthropic)

package modules

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func writeSSE(w http.ResponseWriter, event, data string) {
	f, _ := w.(http.Flusher)
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}
	f.Flush()
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	f.Flush()
}

func TestAIReadSSERejectsTruncatedAndOversize(t *testing.T) {
	for name, body := range map[string]string{
		"truncated": "data: {\"x\":1}\n\n",
		"oversize":  "data: " + strings.Repeat("x", 128) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := aiReadSSE(context.Background(), bufio.NewReader(strings.NewReader(body)), 64, func(aiSSEEvent) (bool, error) { return false, nil })
			if err == nil {
				t.Fatal("accepted incomplete or oversized SSE stream")
			}
		})
	}
}
