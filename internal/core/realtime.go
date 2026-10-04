package core

import (
	"context"

	"encoding/json"
	"github.com/gorilla/websocket"
	"net/http"
	"sync"
	"time"
)

type connection struct {
	socket    *websocket.Conn
	id        Identity
	wake      chan struct{}
	pendingMu sync.Mutex
	pending   map[string]struct{}
	done      chan struct{}
	once      sync.Once
}

// Notifications carry no message payload: the journal is the durable queue.
// At most one notification per subscribed chat is retained between flushes.
func (conn *connection) notify(chat string) {
	conn.pendingMu.Lock()
	if conn.pending == nil {
		conn.pending = make(map[string]struct{})
	}
	conn.pending[chat] = struct{}{}
	conn.pendingMu.Unlock()
	select {
	case conn.wake <- struct{}{}:
	default:
	}
}
func (conn *connection) takePending() map[string]struct{} {
	conn.pendingMu.Lock()
	pending := conn.pending
	conn.pending = nil
	conn.pendingMu.Unlock()
	return pending
}

var websocketWriteBuffers sync.Pool

func (conn *connection) close() { conn.once.Do(func() { close(conn.done); _ = conn.socket.Close() }) }
func (c *Core) DisconnectUser(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, set := range c.connections {
		for conn := range set {
			if conn.id.UserID == user {
				conn.close()
			}
		}
	}
}
func (c *Core) DisconnectDevice(device string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for conn := range c.connections[device] {
		conn.close()
	}
}
func (c *Core) Wake(chat string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for conn := range c.subscribers[chat] {
		conn.notify(chat)
	}
}

// Only subscribed connections are woken. Journal polling also recovers missed notifications.
func (c *Core) subscribe(conn *connection, chat string, enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscribers == nil {
		c.subscribers = map[string]map[*connection]struct{}{}
	}
	if enabled {
		if c.subscribers[chat] == nil {
			c.subscribers[chat] = map[*connection]struct{}{}
		}
		c.subscribers[chat][conn] = struct{}{}
	} else {
		delete(c.subscribers[chat], conn)
		conn.pendingMu.Lock()
		delete(conn.pending, chat)
		conn.pendingMu.Unlock()
		if len(c.subscribers[chat]) == 0 {
			delete(c.subscribers, chat)
		}
	}
}
func (c *Core) websocket(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		allowed := false
		for _, item := range c.Config.Server.Origins {
			if item == origin {
				allowed = true
			}
		}
		if !allowed {
			Error(w, 403, "origin denied")
			return
		}
	}
	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		Error(w, 401, "one-time ticket required")
		return
	}
	var id Identity
	err := c.DB.QueryRowContext(r.Context(), `DELETE FROM tickets WHERE hash=? AND expires>=? RETURNING user_id,device_id`, hashTicket(ticket), time.Now().Unix()).Scan(&id.UserID, &id.DeviceID)
	if err != nil || !c.deviceActive(r, id) {
		Error(w, 401, "invalid or expired ticket")
		return
	}
	c.mu.Lock()
	if c.active >= c.Config.Capacity.MaxConnections {
		c.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		Error(w, 503, "connection capacity reached")
		return
	}
	c.active++
	c.mu.Unlock()
	upgrader := websocket.Upgrader{ReadBufferSize: 1024, WriteBufferSize: 1024, WriteBufferPool: &websocketWriteBuffers, CheckOrigin: func(*http.Request) bool { return true }, HandshakeTimeout: 5 * time.Second}
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		c.mu.Lock()
		c.active--
		c.mu.Unlock()
		return
	}
	conn := &connection{socket: socket, id: id, wake: make(chan struct{}, 1), done: make(chan struct{})}
	c.mu.Lock()
	if c.connections[id.DeviceID] == nil {
		c.connections[id.DeviceID] = map[*connection]struct{}{}
	}
	c.connections[id.DeviceID][conn] = struct{}{}
	c.mu.Unlock()
	// Release the HTTP handler/request stack after hijacking. The connection
	// lifetime belongs to Core, not the canceled HTTP request context.
	go c.runWebsocket(conn)
}

func (c *Core) runWebsocket(conn *connection) {
	socket, id := conn.socket, conn.id
	defer func() {
		conn.close()
		c.mu.Lock()
		delete(c.connections[id.DeviceID], conn)
		if len(c.connections[id.DeviceID]) == 0 {
			delete(c.connections, id.DeviceID)
		}
		c.active--
		c.mu.Unlock()
	}()
	commands := make(chan wsCommand, 8)
	socket.SetReadLimit(4096)
	_ = socket.SetReadDeadline(time.Now().Add(60 * time.Second))
	socket.SetPongHandler(func(string) error { return socket.SetReadDeadline(time.Now().Add(60 * time.Second)) })
	go func() {
		defer conn.close()
		for {
			_, raw, e := socket.ReadMessage()
			if e != nil {
				return
			}
			var cmd wsCommand
			if json.Unmarshal(raw, &cmd) != nil {
				return
			}
			select {
			case commands <- cmd:
			case <-conn.done:
				return
			default:
				return
			}
		}
	}()
	subscriptions := map[string]int64{}
	defer func() {
		for chat := range subscriptions {
			c.subscribe(conn, chat, false)
		}
	}()
	// Idle sockets need maintenance only at ping deadlines. Replay polling
	// is enabled separately only for sockets with subscriptions.
	var replay *time.Ticker
	var replayC <-chan time.Time
	defer func() {
		if replay != nil {
			replay.Stop()
		}
	}()
	updateReplay := func() {
		if len(subscriptions) > 0 && replay == nil {
			replay = time.NewTicker(time.Second)
			replayC = replay.C
		} else if len(subscriptions) == 0 && replay != nil {
			replay.Stop()
			replay = nil
			replayC = nil
		}
	}
	maintenance := time.NewTimer(25 * time.Second)
	defer maintenance.Stop()
	expires := time.NewTimer(15 * time.Minute)
	defer expires.Stop()
	lastPing := time.Now()
	write := func(v any) error {
		_ = socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return socket.WriteJSON(v)
	}
	flush := func(chat string) bool {
		cursor, ok := subscriptions[chat]
		if !ok {
			return true
		}
		ctx, cancel := context.WithTimeout(c.Context, 10*time.Second)
		defer cancel()
		// Bound each turn for fairness, but catch up several pages immediately.
		// A remaining backlog reschedules itself without waiting for the poll.
		for page := 0; page < 10; page++ {
			events, e := c.Events(ctx, id, chat, cursor, 100)
			if e != nil {
				c.subscribe(conn, chat, false)
				delete(subscriptions, chat)
				updateReplay()
				return write(map[string]any{"type": "sync.error", "chat_id": chat, "error": e.Error()}) == nil
			}
			for _, event := range events {
				if write(event) != nil {
					return false
				}
				subscriptions[chat] = event.Seq
				cursor = event.Seq
			}
			if len(events) < 100 {
				return true
			}
		}
		conn.notify(chat)
		return true
	}
	for {
		select {
		case <-c.Context.Done():
			return
		case <-conn.done:
			return
		case <-expires.C:
			return
		case cmd := <-commands:
			if cmd.Type == "unsubscribe" {
				c.subscribe(conn, cmd.ChatID, false)
				delete(subscriptions, cmd.ChatID)
				updateReplay()
				continue
			}
			if cmd.Type != "subscribe" || cmd.After < 0 || len(subscriptions) >= 128 {
				if write(map[string]string{"type": "error", "error": "invalid subscription"}) != nil {
					return
				}
				continue
			}
			if _, _, _, e := c.Member(c.Context, id.UserID, cmd.ChatID); e != nil {
				if write(map[string]string{"type": "error", "error": "membership required"}) != nil {
					return
				}
				continue
			}
			subscriptions[cmd.ChatID] = cmd.After
			c.subscribe(conn, cmd.ChatID, true)
			updateReplay()
			if !flush(cmd.ChatID) {
				return
			}
		case <-conn.wake:
			for chat := range conn.takePending() {
				if !flush(chat) {
					return
				}
			}
		case <-replayC:
			for chat := range subscriptions {
				if !flush(chat) {
					return
				}
			}
		case <-maintenance.C:
			if time.Since(lastPing) >= 25*time.Second {
				_ = socket.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if socket.WriteMessage(websocket.PingMessage, nil) != nil {
					return
				}
				lastPing = time.Now()
			}
			next := time.Until(lastPing.Add(25 * time.Second))
			if next < time.Millisecond {
				next = time.Millisecond
			}
			maintenance.Reset(next)
		}
	}
}

type wsCommand struct {
	Type   string `json:"type"`
	ChatID string `json:"chat_id"`
	After  int64  `json:"after"`
}
