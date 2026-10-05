//go:build qg_ai_endpoint && qg_e2ee && qg_http_tools

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
	"os"
	"strings"
)

func (r *Runtime) makeHTTPHandler(name string, hc HandlerConfig, require bool) (Handler, error) {
	return Handler{Name: name, RequireApproval: require, FnRPC: func(ctx context.Context, req RPCRequest) (json.RawMessage, error) {
		return r.callConfiguredHTTP(ctx, req, hc)
	}}, nil
}

func (r *Runtime) callConfiguredHTTP(ctx context.Context, req RPCRequest, hc HandlerConfig) (json.RawMessage, error) {
	u, err := url.Parse(hc.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("microsafer: invalid configured HTTP handler URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("microsafer: configured HTTP URL must not contain credentials, query, or fragment")
	}
	if !isLoopbackHost(u.Hostname()) && !(r.cfg.Endpoint.AllowExternalPlaintext && hc.AllowPlaintext) {
		return nil, errors.New("microsafer: external HTTP handler requires explicit plaintext opt-in")
	}
	method := strings.ToUpper(hc.Method)
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost && method != http.MethodPut {
		return nil, errors.New("microsafer: unsupported configured HTTP method")
	}
	var body io.Reader
	if method != http.MethodGet {
		body = bytes.NewReader(req.Body)
	}
	outReq, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if method != http.MethodGet {
		outReq.Header.Set("Content-Type", "application/json")
	}
	if hc.KeyEnv != "" {
		if key := strings.TrimSpace(os.Getenv(hc.KeyEnv)); key != "" {
			outReq.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := r.transport.client().Do(outReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("microsafer: configured HTTP handler status %s", resp.Status)
	}
	limit := r.cfg.Endpoint.MaxBodyBytes
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit || !json.Valid(raw) {
		return nil, errors.New("microsafer: configured HTTP handler returned invalid or oversized JSON")
	}
	return raw, nil
}
