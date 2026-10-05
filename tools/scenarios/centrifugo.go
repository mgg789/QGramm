package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type centrifugoAdapter struct {
	cfg       Config
	receive   Receiver
	mu        sync.Mutex
	clients   []*centrifugoClient
	positions []centrifugoPosition
	errors    atomic.Int64
}
type centrifugoPosition struct {
	Offset uint64
	Epoch  string
}
type centrifugoClient struct {
	owner       *centrifugoAdapter
	user        int
	ws          *websocket.Conn
	write       sync.Mutex
	mu          sync.Mutex
	pending     map[uint64]chan centrifugoReply
	next        atomic.Uint64
	intentional atomic.Bool
	done        chan struct{}
}
type centrifugoPub struct {
	Data   json.RawMessage `json:"data"`
	Offset uint64          `json:"offset"`
}
type centrifugoReply struct {
	ID    uint64 `json:"id"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Subscribe *struct {
		Recovered    bool            `json:"recovered"`
		Recoverable  bool            `json:"recoverable"`
		Offset       uint64          `json:"offset"`
		Epoch        string          `json:"epoch"`
		Publications []centrifugoPub `json:"publications"`
	} `json:"subscribe"`
	History *struct {
		Publications []centrifugoPub `json:"publications"`
		Offset       uint64          `json:"offset"`
		Epoch        string          `json:"epoch"`
	} `json:"history"`
	Push *struct {
		Channel    string         `json:"channel"`
		Pub        *centrifugoPub `json:"pub"`
		Disconnect *struct {
			Code int `json:"code"`
		} `json:"disconnect"`
	} `json:"push"`
}

func NewCentrifugo(ctx context.Context, cfg Config, receive Receiver) (Adapter, error) {
	a := &centrifugoAdapter{cfg: cfg, receive: receive, clients: make([]*centrifugoClient, cfg.Users), positions: make([]centrifugoPosition, cfg.Users)}
	e := competitorSetup(ctx, cfg.Users, func(u int) error { return a.open(ctx, u, false) })
	if e != nil {
		a.Close()
		return nil, e
	}
	return a, nil
}
func centrifugoToken(secret string, user int) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": strconv.Itoa(user), "exp": time.Now().Add(time.Hour).Unix()})
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *centrifugoAdapter) open(ctx context.Context, user int, recover bool) error {
	ws, _, e := websocket.DefaultDialer.DialContext(ctx, a.cfg.URL, nil)
	if e != nil {
		return e
	}
	c := &centrifugoClient{owner: a, user: user, ws: ws, pending: make(map[uint64]chan centrifugoReply), done: make(chan struct{})}
	a.mu.Lock()
	a.clients[user] = c
	pos := a.positions[user]
	a.mu.Unlock()
	go c.read()
	_, e = c.command(ctx, "connect", map[string]any{"token": centrifugoToken(a.cfg.Secret, user)})
	if e != nil {
		return e
	}
	if !isReceiver(a.cfg, user) {
		return nil
	}
	args := map[string]any{"channel": fmt.Sprintf("bench.%d", chatFor(a.cfg, user))}
	if recover {
		args["recover"] = true
		args["offset"] = pos.Offset
		args["epoch"] = pos.Epoch
	}
	reply, e := c.command(ctx, "subscribe", args)
	if e != nil {
		return e
	}
	if reply.Subscribe == nil {
		return fmt.Errorf("missing subscribe reply")
	}
	if recover && !reply.Subscribe.Recovered {
		return fmt.Errorf("history recovery failed for receiver %d at offset %d", user, pos.Offset)
	}
	a.mu.Lock()
	a.positions[user].Epoch = reply.Subscribe.Epoch
	a.mu.Unlock()
	for _, pub := range reply.Subscribe.Publications {
		if e = c.deliver(fmt.Sprintf("bench.%d", chatFor(a.cfg, user)), pub); e != nil {
			return e
		}
	}
	return nil
}
func (c *centrifugoClient) command(ctx context.Context, name string, args any) (centrifugoReply, error) {
	id := c.next.Add(1)
	ch := make(chan centrifugoReply, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	c.write.Lock()
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.ws.SetWriteDeadline(deadline)
	e := c.ws.WriteJSON(map[string]any{"id": id, name: args})
	c.write.Unlock()
	if e != nil {
		return centrifugoReply{}, e
	}
	select {
	case reply := <-ch:
		if reply.Error != nil {
			return reply, &SendError{Status: reply.Error.Code, Backpressure: reply.Error.Code == 111, Detail: fmt.Sprintf("centrifugo code %d: %s", reply.Error.Code, reply.Error.Message)}
		}
		return reply, nil
	case <-ctx.Done():
		return centrifugoReply{}, ctx.Err()
	case <-c.done:
		return centrifugoReply{}, fmt.Errorf("websocket closed")
	}
}
func (c *centrifugoClient) read() {
	defer close(c.done)
	for {
		_, data, e := c.ws.ReadMessage()
		if e != nil {
			if !c.intentional.Load() {
				c.owner.errors.Add(1)
			}
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		for {
			var reply centrifugoReply
			e = decoder.Decode(&reply)
			if e == io.EOF {
				break
			}
			if e != nil {
				c.owner.errors.Add(1)
				return
			}
			if reply.ID != 0 {
				c.mu.Lock()
				ch := c.pending[reply.ID]
				c.mu.Unlock()
				if ch != nil {
					ch <- reply
				}
				continue
			}
			if reply.Push != nil && reply.Push.Pub != nil {
				if e = c.deliver(reply.Push.Channel, *reply.Push.Pub); e != nil {
					c.owner.errors.Add(1)
				}
			} else if reply.Push != nil && reply.Push.Disconnect != nil {
				if !c.intentional.Load() {
					c.owner.errors.Add(1)
				}
			} else if reply.Push == nil {
				c.write.Lock()
				_ = c.ws.SetWriteDeadline(time.Now().Add(15 * time.Second))
				e = c.ws.WriteJSON(map[string]any{})
				c.write.Unlock()
				if e != nil && !c.intentional.Load() {
					c.owner.errors.Add(1)
				}
			}
		}
	}
}
func centrifugoPayload(pub centrifugoPub) ([]byte, string, error) {
	var body string
	if e := json.Unmarshal(pub.Data, &body); e != nil {
		return nil, "", e
	}
	payload := []byte(body)
	id, e := competitorPayloadID(payload)
	return payload, id, e
}
func (c *centrifugoClient) deliver(channel string, pub centrifugoPub) error {
	expected := fmt.Sprintf("bench.%d", chatFor(c.owner.cfg, c.user))
	if channel != expected {
		return fmt.Errorf("unexpected channel")
	}
	payload, id, e := centrifugoPayload(pub)
	if e != nil {
		return e
	}
	c.owner.receive(Delivery{ID: id, Chat: chatFor(c.owner.cfg, c.user), Receiver: c.user, Seq: pub.Offset, Payload: payload})
	c.owner.mu.Lock()
	if pub.Offset > c.owner.positions[c.user].Offset {
		c.owner.positions[c.user].Offset = pub.Offset
	}
	c.owner.mu.Unlock()
	return nil
}
func (a *centrifugoAdapter) Send(ctx context.Context, p Publication) error {
	a.mu.Lock()
	c := a.clients[p.Sender]
	a.mu.Unlock()
	if c == nil {
		return fmt.Errorf("sender disconnected")
	}
	_, e := c.command(ctx, "publish", map[string]any{"channel": fmt.Sprintf("bench.%d", p.Chat), "data": string(p.Payload)})
	return e
}
func (a *centrifugoAdapter) History(ctx context.Context, accepted []Publication) (HistoryResult, error) {
	r := HistoryResult{Supported: true}
	expected := make(map[string]Publication, len(accepted))
	for _, p := range accepted {
		expected[p.ID] = p
	}
	seen := make(map[string]bool)
	for chat := 0; chat < a.cfg.Chats; chat++ {
		a.mu.Lock()
		c := a.clients[senderFor(a.cfg, chat)]
		a.mu.Unlock()
		if c == nil {
			return r, fmt.Errorf("history sender disconnected")
		}
		reply, e := c.command(ctx, "history", map[string]any{"channel": fmt.Sprintf("bench.%d", chat), "limit": 1000000})
		if e != nil {
			return r, e
		}
		if reply.History == nil {
			return r, fmt.Errorf("missing history reply")
		}
		if uint64(len(reply.History.Publications)) != reply.History.Offset {
			return r, fmt.Errorf("history truncated or expired for chat %d: have %d, offset %d", chat, len(reply.History.Publications), reply.History.Offset)
		}
		for _, pub := range reply.History.Publications {
			r.Checked++
			payload, id, e := centrifugoPayload(pub)
			p, ok := expected[id]
			if e != nil || !ok {
				r.Unexpected++
				continue
			}
			if seen[id] {
				r.Unexpected++
			}
			seen[id] = true
			if p.Chat != chat || string(p.Payload) != string(payload) {
				r.Corrupt++
			}
		}
	}
	for id := range expected {
		if !seen[id] {
			r.Missing++
		}
	}
	return r, nil
}
func (a *centrifugoAdapter) Disconnect(_ context.Context, users []int) error {
	for _, u := range users {
		a.mu.Lock()
		c := a.clients[u]
		a.clients[u] = nil
		a.mu.Unlock()
		if c != nil {
			c.intentional.Store(true)
			_ = c.ws.Close()
			<-c.done
		}
	}
	return nil
}
func (a *centrifugoAdapter) Reconnect(ctx context.Context, users []int) error {
	return competitorSetup(ctx, len(users), func(i int) error { return a.open(ctx, users[i], true) })
}
func (a *centrifugoAdapter) Close() error {
	for u := range a.clients {
		a.mu.Lock()
		c := a.clients[u]
		a.clients[u] = nil
		a.mu.Unlock()
		if c != nil {
			c.intentional.Store(true)
			_ = c.ws.Close()
			<-c.done
		}
	}
	return nil
}
func (a *centrifugoAdapter) Errors() int64 { return a.errors.Load() }
