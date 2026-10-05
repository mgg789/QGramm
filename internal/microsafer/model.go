//go:build qg_ai_endpoint && qg_e2ee && qg_openai

package microsafer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// CompleteModelStream emits bounded SSE frames as they arrive. The callback
// is intended to encrypt and persist each typed response before returning to
// the provider loop; it is never required to buffer the complete completion.
func (r *Runtime) CompleteModelStream(ctx context.Context, name string, in ChatCompletionRequest, emit ModelResponseFunc) error {
	return r.completeModelStream(ctx, name, in, 0, true, emit)
}

func (r *Runtime) completeModelStream(ctx context.Context, name string, in ChatCompletionRequest, seqOffset uint64, emitNotice bool, emit ModelResponseFunc) error {
	if emit == nil {
		return errors.New("microsafer: model response callback is required")
	}
	m, ok := r.cfg.Models[name]
	if !ok {
		return fmt.Errorf("microsafer: model %q is not configured", name)
	}
	u, e := url.Parse(m.URL)
	if e != nil || u.Host == "" {
		return errors.New("microsafer: invalid configured model URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("microsafer: model URL must not contain credentials, query, or fragment")
	}
	external := !isLoopbackHost(u.Hostname())
	if external && !(r.cfg.Endpoint.AllowExternalPlaintext && m.AllowPlaintext) {
		return errors.New("microsafer: external plaintext model requires explicit endpoint and model opt-in")
	}
	if in.Model != "" && in.Model != m.Model {
		return errors.New("microsafer: model override is not permitted")
	}
	if len(in.Messages) == 0 || len(in.Messages) > 128 {
		return errors.New("microsafer: invalid message count")
	}
	for _, msg := range in.Messages {
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" {
			return errors.New("microsafer: invalid message role")
		}
		if len(msg.Content) > 1<<20 {
			return errors.New("microsafer: message too large")
		}
	}
	in.Model = m.Model
	in.Stream = true
	if m.MaxTokens > 0 && (in.MaxTokens == 0 || in.MaxTokens > m.MaxTokens) {
		in.MaxTokens = m.MaxTokens
	}
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	if len(b) > r.cfg.Endpoint.MaxBodyBytes {
		return errors.New("microsafer: model request exceeds limit")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := r.transport.client().Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("microsafer: model status %s", resp.Status)
	}
	max := m.MaxBodyBytes
	if max <= 0 || max > r.cfg.Endpoint.MaxBodyBytes {
		max = r.cfg.Endpoint.MaxBodyBytes
	}
	// Read one byte past the configured body bound.  A response truncated at
	// exactly max bytes must not be accepted as a complete stream unless the
	// provider actually sent its [DONE] marker.
	scan := bufio.NewScanner(io.LimitReader(resp.Body, int64(max)+1))
	scan.Buffer(make([]byte, 4096), minInt(max, 256<<10))
	var seq uint64 = seqOffset
	seenDone := false
	var pending *RPCResponse
	if external && emitNotice {
		seq = 1
		body, _ := json.Marshal(ExternalPlaintextNotice{Kind: "external_plaintext", URL: safeNoticeURL(m.URL), Reason: "configured model endpoint receives request plaintext"})
		if e = emit(RPCResponse{Kind: "notice", Seq: 1, Body: body}); e != nil {
			return e
		}
	}
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			seenDone = true
			break
		}
		if data == "" {
			continue
		}
		if len(data) > r.cfg.Endpoint.MaxFrameBytes {
			return errors.New("microsafer: SSE frame exceeds limit")
		}
		seq++
		cur := RPCResponse{Kind: "delta", Seq: seq, Body: json.RawMessage(data)}
		if pending != nil {
			if e = emit(*pending); e != nil {
				return e
			}
		}
		pending = &cur
	}
	if e = scan.Err(); e != nil {
		return e
	}
	if !seenDone {
		return errors.New("microsafer: model SSE stream did not terminate with [DONE]")
	}
	if pending != nil {
		pending.Kind = "result"
		pending.Final = true
		if e = emit(*pending); e != nil {
			return e
		}
	} else {
		if e = emit(RPCResponse{Kind: "result", Seq: seq + 1, Final: true, Body: json.RawMessage(`{}`)}); e != nil {
			return e
		}
	}
	return nil
}

// CompleteModel calls only a model named in the deployment TOML.  It rejects
// caller supplied URLs/models and bounds both the request and each SSE frame.
func (r *Runtime) CompleteModel(ctx context.Context, name string, in ChatCompletionRequest) ([]RPCResponse, error) {
	m, ok := r.cfg.Models[name]
	if !ok {
		return nil, fmt.Errorf("microsafer: model %q is not configured", name)
	}
	modelURL, parseErr := url.Parse(m.URL)
	if parseErr != nil || modelURL.Host == "" {
		return nil, errors.New("microsafer: invalid configured model URL")
	}
	if modelURL.User != nil || modelURL.RawQuery != "" || modelURL.Fragment != "" {
		return nil, errors.New("microsafer: model URL must not contain credentials, query, or fragment")
	}
	external := !isLoopbackHost(modelURL.Hostname())
	if external && !(r.cfg.Endpoint.AllowExternalPlaintext && m.AllowPlaintext) {
		return nil, errors.New("microsafer: external plaintext model requires explicit endpoint and model opt-in")
	}
	if in.Model != "" && in.Model != m.Model {
		return nil, errors.New("microsafer: model override is not permitted")
	}
	in.Model = m.Model
	if len(in.Messages) == 0 || len(in.Messages) > 128 {
		return nil, errors.New("microsafer: invalid message count")
	}
	for _, msg := range in.Messages {
		if msg.Role != "system" && msg.Role != "user" && msg.Role != "assistant" {
			return nil, errors.New("microsafer: invalid message role")
		}
		if len(msg.Content) > 1<<20 {
			return nil, errors.New("microsafer: message too large")
		}
	}
	in.Stream = true
	if m.MaxTokens > 0 && (in.MaxTokens == 0 || in.MaxTokens > m.MaxTokens) {
		in.MaxTokens = m.MaxTokens
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	if len(b) > r.cfg.Endpoint.MaxBodyBytes {
		return nil, errors.New("microsafer: model request exceeds limit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.transport.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("microsafer: model status %s", resp.Status)
	}
	max := m.MaxBodyBytes
	if max <= 0 || max > r.cfg.Endpoint.MaxBodyBytes {
		max = r.cfg.Endpoint.MaxBodyBytes
	}
	// See CompleteModelStream: exact-bound truncation is not a valid completion.
	scan := bufio.NewScanner(io.LimitReader(resp.Body, int64(max)+1))
	scan.Buffer(make([]byte, 4096), minInt(max, 256<<10))
	var out []RPCResponse
	var seq uint64
	var dataFrames int
	seenDone := false
	if external {
		seq = 1
		notice, _ := json.Marshal(ExternalPlaintextNotice{Kind: "external_plaintext", URL: safeNoticeURL(m.URL), Reason: "configured model endpoint receives request plaintext"})
		out = append(out, RPCResponse{Kind: "notice", Seq: 1, Body: notice})
	}
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			seenDone = true
			break
		}
		if data == "" {
			continue
		}
		if len(data) > r.cfg.Endpoint.MaxFrameBytes {
			return nil, errors.New("microsafer: SSE frame exceeds limit")
		}
		seq++
		dataFrames++
		out = append(out, RPCResponse{Kind: "delta", Seq: seq, Body: json.RawMessage(data)})
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if !seenDone {
		return nil, errors.New("microsafer: model SSE stream did not terminate with [DONE]")
	}
	if dataFrames > 0 {
		out[len(out)-1].Final = true
		out[len(out)-1].Kind = "result"
	} else {
		out = append(out, RPCResponse{Kind: "result", Seq: seq + 1, Final: true, Body: json.RawMessage(`{}`)})
	}
	return out, nil
}
