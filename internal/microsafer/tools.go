//go:build qg_ai_endpoint && qg_e2ee && qg_mcp

package microsafer

import (
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

type ToolEndpoint struct {
	Name           string
	URL            string
	AllowPlaintext bool
}
type ToolPlaintextNotice struct {
	Kind      string `json:"kind"`
	Endpoint  string `json:"endpoint"`
	Operation string `json:"operation"`
}

func (r *Runtime) CallTool(ctx context.Context, endpoint ToolEndpoint, operation string, body json.RawMessage) (json.RawMessage, *ToolPlaintextNotice, error) {
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Host == "" {
		return nil, nil, errors.New("microsafer: invalid configured tool URL")
	}
	if !isLoopbackHost(u.Hostname()) && !r.cfg.Endpoint.AllowExternalPlaintext {
		return nil, nil, errors.New("microsafer: external plaintext tools require endpoint opt-in")
	}
	return CallTool(ctx, endpoint, operation, body, r.transport.client(), r.cfg.Endpoint.MaxBodyBytes)
}

// CallTool supports only the MCP initialize/tools-call methods against a
// statically configured endpoint.  The endpoint URL is never taken from RPC
// body data.
func CallTool(ctx context.Context, endpoint ToolEndpoint, operation string, body json.RawMessage, client *http.Client, maxBody int) (json.RawMessage, *ToolPlaintextNotice, error) {
	if endpoint.Name == "" || endpoint.URL == "" {
		return nil, nil, errors.New("microsafer: incomplete tool endpoint")
	}
	if operation != "initialize" && operation != "tools/call" {
		return nil, nil, errors.New("microsafer: unsupported MCP operation")
	}
	if len(body) > maxBody && maxBody > 0 {
		return nil, nil, errors.New("microsafer: tool body exceeds limit")
	}
	u, err := url.Parse(endpoint.URL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return nil, nil, errors.New("microsafer: tool endpoint must be https or loopback http")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, nil, errors.New("microsafer: tool endpoint must not contain credentials, query, or fragment")
	}
	b, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}{"2.0", endpoint.Name, operation, body})
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("microsafer: tool status %s", resp.Status)
	}
	limit := maxBody
	if limit <= 0 {
		limit = 1 << 20
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, nil, err
	}
	if len(out) > limit {
		return nil, nil, errors.New("microsafer: tool response exceeds limit")
	}
	var check any
	if err = json.Unmarshal(out, &check); err != nil {
		return nil, nil, errors.New("microsafer: tool returned invalid JSON")
	}
	var notice *ToolPlaintextNotice
	if endpoint.AllowPlaintext && !isLoopbackHost(u.Hostname()) {
		notice = &ToolPlaintextNotice{Kind: "external_plaintext", Endpoint: endpoint.Name, Operation: operation}
	}
	return out, notice, nil
}

func fixedEndpointURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Host != "" && strings.Contains(u.Scheme, "http")
}
