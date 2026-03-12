package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"qgramm/backend/internal/services"
)

type contextKey string

const identityKey contextKey = "identity"
const debugBodyPreviewLimit = 4096

var (
	jsonSecretValuePattern = regexp.MustCompile(`(?i)"(access_token|refresh_token|password|code)"\s*:\s*"[^"]*"`)
	formSecretValuePattern = regexp.MustCompile(`(?i)(^|[&;])(access_token|refresh_token|password|code)=([^&;]*)`)
)

func requestDebugLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		requestID := chimiddleware.GetReqID(r.Context())

		requestBodyCapture := &bodyCapture{limit: debugBodyPreviewLimit}
		if r.Body != nil {
			r.Body = &bodyLoggingReadCloser{ReadCloser: r.Body, capture: requestBodyCapture}
		}

		responseBodyCapture := &bodyCapture{limit: debugBodyPreviewLimit}
		loggingWriter := &loggingResponseWriter{
			ResponseWriter: w,
			capture:        responseBodyCapture,
		}

		next.ServeHTTP(loggingWriter, r)

		statusCode := loggingWriter.Status()
		requestBodyPreview := bodyPreview(requestBodyCapture, r.Header.Get("Content-Type"))
		responseBodyPreview := bodyPreview(responseBodyCapture, loggingWriter.Header().Get("Content-Type"))

		query := sanitizeQuery(r.URL.RawQuery)
		remoteIP := strings.TrimSpace(r.RemoteAddr)

		log.Printf(
			"[debug-http] id=%s ip=%s method=%s path=%s query=%q status=%d duration_ms=%d req_headers=%q req_body=%q resp_headers=%q resp_body=%q",
			requestID,
			remoteIP,
			r.Method,
			r.URL.Path,
			query,
			statusCode,
			time.Since(startedAt).Milliseconds(),
			formatHeadersForLog(r.Header),
			requestBodyPreview,
			formatHeadersForLog(loggingWriter.Header()),
			responseBodyPreview,
		)

		if statusCode >= http.StatusBadRequest {
			log.Printf(
				"[debug-http-error] id=%s method=%s path=%s status=%d req_body=%q resp_body=%q",
				requestID,
				r.Method,
				r.URL.Path,
				statusCode,
				requestBodyPreview,
				responseBodyPreview,
			)
		}
	})
}

func withIdentity(ctx context.Context, identity services.Identity) context.Context {
	return context.WithValue(ctx, identityKey, identity)
}

func identityFromContext(ctx context.Context) (services.Identity, bool) {
	value := ctx.Value(identityKey)
	if value == nil {
		return services.Identity{}, false
	}
	identity, ok := value.(services.Identity)
	return identity, ok
}

func authMiddleware(svc *services.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractAccessToken(r)
			if token == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing access token"})
				return
			}

			identity, err := svc.AuthenticateAccessToken(r.Context(), token)
			if err != nil {
				handleError(w, err)
				return
			}

			next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), identity)))
		})
	}
}

func rootMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := identityFromContext(r.Context())
		if !ok || !identity.IsRoot {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "root access required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractAccessToken(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return strings.TrimSpace(raw[7:])
	}

	if query := strings.TrimSpace(r.URL.Query().Get("access_token")); query != "" {
		return query
	}

	return ""
}

type bodyLoggingReadCloser struct {
	io.ReadCloser
	capture *bodyCapture
}

func (c *bodyLoggingReadCloser) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 && c.capture != nil {
		c.capture.append(p[:n])
	}
	return n, err
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	capture    *bodyCapture
}

func (w *loggingResponseWriter) Status() int {
	if w.statusCode == 0 {
		return http.StatusOK
	}
	return w.statusCode
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	if n > 0 && w.capture != nil {
		w.capture.append(p[:n])
	}
	return n, err
}

func (w *loggingResponseWriter) Flush() {
	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		return
	}
	flusher.Flush()
}

func (w *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (w *loggingResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type bodyCapture struct {
	limit     int
	totalSize int
	buf       bytes.Buffer
	truncated bool
}

func (c *bodyCapture) append(p []byte) {
	c.totalSize += len(p)
	if c.limit <= 0 || c.truncated {
		return
	}

	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return
	}
	if len(p) <= remaining {
		_, _ = c.buf.Write(p)
		return
	}

	_, _ = c.buf.Write(p[:remaining])
	c.truncated = true
}

func bodyPreview(capture *bodyCapture, contentType string) string {
	if capture == nil || capture.totalSize == 0 {
		return "empty"
	}

	if !isTextContent(contentType, capture.buf.Bytes()) {
		if capture.truncated {
			return fmt.Sprintf("binary body (%d bytes, preview truncated)", capture.totalSize)
		}
		return fmt.Sprintf("binary body (%d bytes)", capture.totalSize)
	}

	preview := strings.ReplaceAll(capture.buf.String(), "\n", "\\n")
	preview = strings.ReplaceAll(preview, "\r", "\\r")
	preview = redactBodySecrets(preview)

	if capture.truncated {
		return fmt.Sprintf("%s ... (truncated, total=%d bytes)", preview, capture.totalSize)
	}
	return preview
}

func formatHeadersForLog(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}

	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		values := headers.Values(key)
		if len(values) == 0 {
			continue
		}

		value := strings.Join(values, ",")
		if isSensitiveHeader(key) {
			value = "***"
		}
		pairs = append(pairs, fmt.Sprintf("%s=%s", key, value))
	}

	return strings.Join(pairs, ";")
}

func sanitizeQuery(rawQuery string) string {
	if strings.TrimSpace(rawQuery) == "" {
		return ""
	}

	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}

	for key := range values {
		if isSensitiveKey(key) {
			values.Set(key, "***")
		}
	}

	return values.Encode()
}

func isSensitiveHeader(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func isSensitiveKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "access_token", "token", "authorization", "password", "code":
		return true
	default:
		return false
	}
}

func isTextContent(contentType string, body []byte) bool {
	normalized := strings.ToLower(strings.TrimSpace(contentType))
	if idx := strings.Index(normalized, ";"); idx >= 0 {
		normalized = strings.TrimSpace(normalized[:idx])
	}

	if normalized == "" {
		return utf8.Valid(body)
	}

	switch {
	case strings.HasPrefix(normalized, "text/"):
		return utf8.Valid(body)
	case normalized == "application/json":
		return utf8.Valid(body)
	case strings.HasSuffix(normalized, "+json"):
		return utf8.Valid(body)
	case normalized == "application/x-www-form-urlencoded":
		return utf8.Valid(body)
	case normalized == "application/xml":
		return utf8.Valid(body)
	case strings.HasSuffix(normalized, "+xml"):
		return utf8.Valid(body)
	default:
		return false
	}
}

func redactBodySecrets(text string) string {
	redactedJSON := jsonSecretValuePattern.ReplaceAllString(text, `"$1":"***"`)
	return formSecretValuePattern.ReplaceAllString(redactedJSON, `$1$2=***`)
}
