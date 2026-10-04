//go:build qg_openai || qg_anthropic

package modules

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/mgg789/QGramm/internal/config"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type aiRequester func(context.Context, string, map[string]string, any, bool) ([]byte, error)

type aiLimitKey struct{}
type aiRequestLimits struct {
	timeout  time.Duration
	response int
}

func aiLimits(settings config.AI) aiRequestLimits {
	limits := aiRequestLimits{45 * time.Second, 1 << 20}
	if settings.TimeoutSeconds > 0 && settings.TimeoutSeconds < 45 {
		limits.timeout = time.Duration(settings.TimeoutSeconds) * time.Second
	}
	if settings.MaxResponseBytes > 0 && settings.MaxResponseBytes < 1<<20 {
		limits.response = settings.MaxResponseBytes
	}
	return limits
}
func aiContextLimits(ctx context.Context) aiRequestLimits {
	if limits, ok := ctx.Value(aiLimitKey{}).(aiRequestLimits); ok {
		return limits
	}
	return aiLimits(config.AI{})
}
func aiToolContext(ctx context.Context, tool config.Tool) (context.Context, context.CancelFunc) {
	limits := aiContextLimits(ctx)
	if tool.TimeoutSeconds > 0 && time.Duration(tool.TimeoutSeconds)*time.Second < limits.timeout {
		limits.timeout = time.Duration(tool.TimeoutSeconds) * time.Second
	}
	if tool.MaxResponseBytes > 0 && tool.MaxResponseBytes < limits.response {
		limits.response = tool.MaxResponseBytes
	}
	ctx = context.WithValue(ctx, aiLimitKey{}, limits)
	return context.WithTimeout(ctx, limits.timeout)
}

// Preserve limits even when a test adapter supplies a requester directly.
func aiBoundRequester(request aiRequester) aiRequester {
	return func(ctx context.Context, raw string, headers map[string]string, body any, private bool) ([]byte, error) {
		limits := aiContextLimits(ctx)
		ctx, cancel := context.WithTimeout(ctx, limits.timeout)
		defer cancel()
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > 1<<20 {
			return nil, errors.New("request too large or unencodable")
		}
		data, err := request(ctx, raw, headers, body, private)
		if err == nil && len(data) > limits.response {
			return nil, errors.New("external response too large")
		}
		return data, err
	}
}

func aiPublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// Each call resolves once and dials only validated addresses. Redirects are
// rejected, so provider/tool credentials cannot migrate to another origin.
func aiRequest(ctx context.Context, raw string, headers map[string]string, body any, allowPrivate bool) ([]byte, error) {
	limits := aiContextLimits(ctx)
	ctx, cancel := context.WithTimeout(ctx, limits.timeout)
	defer cancel()
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && allowPrivate)) {
		return nil, errors.New("egress URL denied")
	}
	addresses, e := net.DefaultResolver.LookupIPAddr(ctx, u.Hostname())
	if e != nil || len(addresses) == 0 {
		return nil, errors.New("egress resolution failed")
	}
	for _, a := range addresses {
		if !allowPrivate && !aiPublicIP(a.IP) {
			return nil, errors.New("egress address denied")
		}
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}, ResponseHeaderTimeout: 20 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		var last error
		for _, a := range addresses {
			conn, e := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(a.IP.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: limits.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	data, e := json.Marshal(body)
	if e != nil {
		return nil, errors.New("request encoding failed")
	}
	if len(data) > 1<<20 {
		return nil, errors.New("request too large")
	}
	method := "POST"
	if m, ok := headers[":method"]; ok {
		method = m
	}
	req, e := http.NewRequestWithContext(ctx, method, raw, bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if !strings.HasPrefix(key, ":") {
			req.Header.Set(key, value)
		}
	}
	response, e := client.Do(req)
	if e != nil {
		return nil, errors.New("external request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("external service rejected request")
	}
	if session := response.Header.Get("Mcp-Session-Id"); session != "" {
		headers[":session"] = session
	}
	if response.StatusCode == http.StatusAccepted {
		return nil, nil
	}
	if strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") && strings.Contains(headers["Accept"], "text/event-stream") {
		reader := &io.LimitedReader{R: response.Body, N: int64(limits.response) + 1}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, min(4096, limits.response)), limits.response)
		var event strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
				event.WriteByte('\n')
			}
			if line == "" && event.Len() > 0 {
				if reader.N == 0 || event.Len() > limits.response {
					return nil, errors.New("external response too large")
				}
				candidate := []byte(strings.TrimSpace(event.String()))
				event.Reset()
				var rpc map[string]json.RawMessage
				if json.Unmarshal(candidate, &rpc) == nil && rpc["id"] != nil {
					return candidate, nil
				}
			}
		}
		return nil, errors.New("MCP response event missing")
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		return nil, errors.New("external response must be application/json")
	}
	data, e = io.ReadAll(io.LimitReader(response.Body, int64(limits.response)+1))
	if e != nil || len(data) > limits.response {
		return nil, errors.New("external response too large or unreadable")
	}
	return data, nil
}
