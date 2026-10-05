package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/mgg789/QGramm/internal/cryptoenc"
)

type qMessage struct {
	ID        string              `json:"id"`
	Chat      string              `json:"chat_id"`
	Operation string              `json:"operation_id"`
	Seq       uint64              `json:"seq"`
	Envelope  *cryptoenc.Envelope `json:"envelope"`
}
type qSocket struct {
	mu          sync.Mutex
	conn        *websocket.Conn
	done        chan struct{}
	intentional bool
	cursor      uint64
}
type qGramm struct {
	cfg          Config
	receive      Receiver
	http         *http.Client
	private      ed25519.PrivateKey
	management   string
	names, chats []string
	engines      []*cryptoenc.Engine
	serverKey    []byte
	sockets      []qSocket
	errors       atomic.Int64
	closing      atomic.Bool
}

func NewQGramm(ctx context.Context, cfg Config, receive Receiver) (Adapter, error) {
	if cfg.Users < cfg.Chats*(cfg.Fanout+1) || cfg.Chats < 1 || cfg.Fanout < 1 || (cfg.ResponseMode != "full" && cfg.ResponseMode != "minimal") {
		return nil, fmt.Errorf("invalid QGramm topology or response mode")
	}
	raw, err := os.ReadFile(cfg.EnvFile)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			env[k] = strings.TrimSpace(v)
		}
	}
	private, err := base64.StdEncoding.DecodeString(env["QGRAMM_BENCH_SIGNING_KEY"])
	if err != nil || len(private) != ed25519.PrivateKeySize || env["QGRAMM_MANAGEMENT_SECRET"] == "" {
		return nil, fmt.Errorf("invalid synthetic QGramm credentials")
	}
	q := &qGramm{cfg: cfg, receive: receive, private: ed25519.PrivateKey(private), management: env["QGRAMM_MANAGEMENT_SECRET"], names: make([]string, cfg.Users), chats: make([]string, cfg.Chats), engines: make([]*cryptoenc.Engine, cfg.Users), sockets: make([]qSocket, cfg.Users), http: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128}}}
	success := false
	defer func() {
		if !success {
			q.Close()
		}
	}()
	seed := make([]byte, 8)
	if _, err = rand.Read(seed); err != nil {
		return nil, err
	}
	prefix := "scenario-" + base64.RawURLEncoding.EncodeToString(seed) + "-"
	for i := range q.names {
		q.names[i] = fmt.Sprintf("%su%d", prefix, i)
		master, key := make([]byte, 32), make([]byte, 32)
		if _, err = rand.Read(master); err != nil {
			return nil, err
		}
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		q.engines[i], err = cryptoenc.New(master, key)
		if err != nil {
			return nil, err
		}
	}
	if err = q.parallel(ctx, cfg.Users, func(i int) error {
		if e := q.provision(ctx, "PUT", "/management/v1/users/"+q.names[i], map[string]bool{"disabled": false}); e != nil {
			return e
		}
		return q.provision(ctx, "PUT", "/management/v1/users/"+q.names[i]+"/devices/"+q.names[i], map[string]string{"public_key": q.engines[i].PublicKey()})
	}); err != nil {
		return nil, err
	}
	if err = q.parallel(ctx, cfg.Chats, func(i int) error {
		q.chats[i] = fmt.Sprintf("%sc%d", prefix, i)
		route := "/management/v1/chats/direct"
		if cfg.Fanout > 1 {
			route = "/management/v1/chats/groups"
		}
		start := senderFor(cfg, i)
		return q.provision(ctx, "POST", route, map[string]any{"id": q.chats[i], "members": q.names[start : start+cfg.Fanout+1]})
	}); err != nil {
		return nil, err
	}
	body, status, err := q.request(ctx, "GET", "/v1/capabilities", 0, nil)
	if err != nil || status != 200 {
		return nil, fmt.Errorf("QGramm capabilities unavailable: status %d", status)
	}
	var caps struct {
		Key string `json:"server_key"`
	}
	if json.Unmarshal(body, &caps) != nil {
		return nil, fmt.Errorf("invalid QGramm capabilities")
	}
	q.serverKey, err = base64.StdEncoding.DecodeString(caps.Key)
	if err != nil || len(q.serverKey) != 32 {
		return nil, fmt.Errorf("invalid QGramm server HPKE key")
	}
	if err = q.parallel(ctx, cfg.Users, func(i int) error { return q.connect(ctx, i) }); err != nil {
		return nil, err
	}
	success = true
	return q, nil
}

func (q *qGramm) parallel(ctx context.Context, n int, fn func(int) error) error {
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					once.Do(func() { first = ctx.Err() })
					continue
				}
				if err := fn(i); err != nil {
					once.Do(func() { first = err })
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			once.Do(func() { first = ctx.Err() })
		}
	}
	close(jobs)
	wg.Wait()
	return first
}
func (q *qGramm) token(index int) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{"iss": "qgramm", "aud": "qgramm", "sub": q.names[index], "device_id": q.names[index], "iat": now.Add(-5 * time.Second).Unix(), "exp": now.Add(14 * time.Minute).Unix()}).SignedString(q.private)
}
func (q *qGramm) request(ctx context.Context, method, path string, index int, body any) ([]byte, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(q.cfg.URL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-Proto", "https")
	token := q.management
	if index >= 0 {
		token, err = q.token(index)
		if err != nil {
			return nil, 0, err
		}
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if q.cfg.ResponseMode == "minimal" && method == "POST" && strings.HasSuffix(path, "/messages") {
		r.Header.Set("Prefer", "return=minimal")
	}
	response, err := q.http.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	return data, response.StatusCode, err
}
func (q *qGramm) provision(ctx context.Context, method, path string, body any) error {
	for retry := 0; retry < 20; retry++ {
		_, status, err := q.request(ctx, method, path, -1, body)
		if err == nil && (status == 200 || status == 201) {
			return nil
		}
		if err == nil && status != 429 && status != 503 {
			return fmt.Errorf("QGramm provisioning status %d", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return fmt.Errorf("QGramm provisioning exhausted retries")
}
func (q *qGramm) connect(ctx context.Context, index int) error {
	s := &q.sockets[index]
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.closing.Load() {
		return fmt.Errorf("QGramm adapter closed")
	}
	if s.conn != nil {
		return fmt.Errorf("QGramm socket already connected")
	}
	var conn *websocket.Conn
	for retry := 0; retry < 20; retry++ {
		data, status, err := q.request(ctx, "POST", "/v1/ws-tickets", index, nil)
		if err == nil && status == 201 {
			var ticket struct {
				Ticket string `json:"ticket"`
			}
			if json.Unmarshal(data, &ticket) != nil || ticket.Ticket == "" {
				return fmt.Errorf("invalid QGramm WebSocket ticket")
			}
			endpoint, err := url.Parse(strings.TrimRight(q.cfg.URL, "/") + "/v1/ws")
			if err != nil {
				return err
			}
			if endpoint.Scheme == "https" {
				endpoint.Scheme = "wss"
			} else {
				endpoint.Scheme = "ws"
			}
			query := endpoint.Query()
			query.Set("ticket", ticket.Ticket)
			endpoint.RawQuery = query.Encode()
			conn, _, err = websocket.DefaultDialer.DialContext(ctx, endpoint.String(), http.Header{"X-Forwarded-Proto": []string{"https"}})
			if err == nil {
				break
			}
		} else if err == nil && status != 429 && status != 503 {
			return fmt.Errorf("QGramm ticket status %d", status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	if conn == nil {
		return fmt.Errorf("QGramm connection exhausted retries")
	}
	if isReceiver(q.cfg, index) {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := conn.WriteJSON(map[string]any{"type": "subscribe", "chat_id": q.chats[chatFor(q.cfg, index)], "after": s.cursor}); err != nil {
			conn.Close()
			return err
		}
	}
	s.conn = conn
	s.done = make(chan struct{})
	s.intentional = false
	go q.read(index, conn, s.done)
	return nil
}
func (q *qGramm) decrypt(index int, m qMessage) ([]byte, error) {
	chat := chatFor(q.cfg, index)
	if !isReceiver(q.cfg, index) || m.Chat != q.chats[chat] || m.ID == "" || m.Operation == "" || m.Seq == 0 || m.Envelope == nil {
		return nil, fmt.Errorf("invalid QGramm message projection")
	}
	// Inbound command envelopes bind the client operation; recipient projections
	// bind the committed server message ID (core.projectStoredMessages).
	return q.engines[index].OpenEnvelope(*m.Envelope, cryptoenc.Binding(m.Chat, q.names[index], q.names[index], m.ID))
}
func (q *qGramm) read(index int, conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	s := &q.sockets[index]
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			s.mu.Lock()
			intentional := s.intentional
			s.mu.Unlock()
			if !intentional && !q.closing.Load() {
				q.errors.Add(1)
			}
			return
		}
		var event struct {
			Type string   `json:"type"`
			Chat string   `json:"chat_id"`
			Seq  uint64   `json:"seq"`
			Data qMessage `json:"data"`
		}
		if json.Unmarshal(raw, &event) != nil || event.Type == "error" || event.Type == "sync.error" {
			q.errors.Add(1)
			continue
		}
		if event.Type != "message.created" {
			continue
		}
		payload, err := q.decrypt(index, event.Data)
		if err != nil || event.Chat != event.Data.Chat || event.Seq != event.Data.Seq {
			q.errors.Add(1)
			continue
		}
		q.receive(Delivery{ID: event.Data.Operation, Chat: chatFor(q.cfg, index), Receiver: index, Seq: event.Seq, Payload: payload})
		s.mu.Lock()
		if event.Seq > s.cursor {
			s.cursor = event.Seq
		}
		s.mu.Unlock()
	}
}
func validateQAck(raw []byte, minimal bool, chat, op string) error {
	var m qMessage
	if minimal {
		var r struct {
			Status  string `json:"status"`
			Receipt struct {
				ID        string `json:"message_id"`
				Chat      string `json:"chat_id"`
				Operation string `json:"operation_id"`
				Seq       uint64 `json:"seq"`
			} `json:"receipt"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Status != "accepted" {
			return fmt.Errorf("invalid QGramm receipt")
		}
		m = qMessage{ID: r.Receipt.ID, Chat: r.Receipt.Chat, Operation: r.Receipt.Operation, Seq: r.Receipt.Seq}
	} else {
		var r struct {
			Status  string   `json:"status"`
			Message qMessage `json:"message"`
		}
		if json.Unmarshal(raw, &r) != nil || r.Status != "accepted" {
			return fmt.Errorf("invalid QGramm message ACK")
		}
		m = r.Message
	}
	if m.ID == "" || m.Chat != chat || m.Operation != op || m.Seq == 0 {
		return fmt.Errorf("invalid QGramm acknowledgement identity")
	}
	return nil
}
func (q *qGramm) Send(ctx context.Context, p Publication) error {
	if p.Chat < 0 || p.Chat >= len(q.chats) || p.Sender != senderFor(q.cfg, p.Chat) {
		return fmt.Errorf("invalid QGramm publication topology")
	}
	envelope, err := cryptoenc.SealEnvelope(q.serverKey, p.Payload, cryptoenc.Binding(q.chats[p.Chat], q.names[p.Sender], q.names[p.Sender], p.ID))
	if err != nil {
		return err
	}
	data, status, err := q.request(ctx, "POST", "/v1/chats/"+q.chats[p.Chat]+"/messages", p.Sender, map[string]any{"operation_id": p.ID, "envelope": envelope})
	if err != nil {
		return err
	}
	if status != 201 {
		return &SendError{Status: status, Backpressure: status == 429 || status == 503, Detail: fmt.Sprintf("QGramm send status %d", status)}
	}
	return validateQAck(data, q.cfg.ResponseMode == "minimal", q.chats[p.Chat], p.ID)
}
func (q *qGramm) History(ctx context.Context, accepted []Publication) (HistoryResult, error) {
	result := HistoryResult{Supported: true}
	wanted := make(map[string]Publication, len(accepted))
	for _, p := range accepted {
		wanted[p.ID] = p
	}
	seen := map[string]bool{}
	pageLimit := qHistoryPageLimit(q.cfg.PayloadBytes)
	for chat := range q.chats {
		receiver := senderFor(q.cfg, chat) + 1
		after := uint64(0)
		for {
			raw, status, err := q.request(ctx, "GET", fmt.Sprintf("/v1/chats/%s/messages?after=%d&limit=%d", q.chats[chat], after, pageLimit), receiver, nil)
			if err != nil {
				return result, err
			}
			if status != 200 {
				return result, fmt.Errorf("QGramm history status %d", status)
			}
			var messages []qMessage
			if json.Unmarshal(raw, &messages) != nil {
				return result, fmt.Errorf("invalid QGramm history JSON")
			}
			if len(messages) == 0 {
				break
			}
			for _, m := range messages {
				result.Checked++
				if m.Seq <= after {
					return result, fmt.Errorf("QGramm history sequence failed")
				}
				after = m.Seq
				p, ok := wanted[m.Operation]
				if !ok {
					result.Unexpected++
					continue
				}
				if seen[m.Operation] {
					result.Corrupt++
					continue
				}
				seen[m.Operation] = true
				payload, err := q.decrypt(receiver, m)
				if err != nil || p.Chat != chat || !bytes.Equal(payload, p.Payload) {
					result.Corrupt++
				}
			}
		}
	}
	for id := range wanted {
		if !seen[id] {
			result.Missing++
		}
	}
	return result, nil
}

// Bound generator memory while checking history for large encrypted payloads.
// Base64/JSON expansion is below the conservative 2x payload+4KiB allowance.
func qHistoryPageLimit(payload int) int {
	if payload < 0 {
		return 1
	}
	limit := (8 << 20) / (payload*2 + 4096)
	if limit < 1 {
		return 1
	}
	if limit > 200 {
		return 200
	}
	return limit
}
func (q *qGramm) disconnect(ctx context.Context, index int) error {
	if index < 0 || index >= len(q.sockets) {
		return fmt.Errorf("invalid QGramm socket index")
	}
	s := &q.sockets[index]
	s.mu.Lock()
	conn, done := s.conn, s.done
	s.intentional = true
	s.mu.Unlock()
	if conn == nil {
		return nil
	}
	conn.Close()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
	}
	s.mu.Unlock()
	return nil
}
func (q *qGramm) Disconnect(ctx context.Context, indices []int) error {
	return q.parallel(ctx, len(indices), func(i int) error { return q.disconnect(ctx, indices[i]) })
}
func (q *qGramm) Reconnect(ctx context.Context, indices []int) error {
	return q.parallel(ctx, len(indices), func(i int) error {
		if indices[i] < 0 || indices[i] >= len(q.sockets) {
			return fmt.Errorf("invalid QGramm socket index")
		}
		return q.connect(ctx, indices[i])
	})
}
func (q *qGramm) Errors() int64 { return q.errors.Load() }
func (q *qGramm) Close() error {
	q.closing.Store(true)
	for i := range q.sockets {
		if err := q.disconnect(context.Background(), i); err != nil {
			return err
		}
	}
	q.http.CloseIdleConnections()
	return nil
}
