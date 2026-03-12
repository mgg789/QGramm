package realtime

import (
    "encoding/json"
    "log"
    "sync"
    "time"

    "github.com/google/uuid"
    "github.com/gorilla/websocket"
)

type Event struct {
    Type      string         `json:"type"`
    UserID    string         `json:"user_id,omitempty"`
    Timestamp time.Time      `json:"timestamp"`
    Payload   map[string]any `json:"payload"`
}

type client struct {
    conn   *websocket.Conn
    userID uuid.UUID
    send   chan []byte
}

type Hub struct {
    mu      sync.RWMutex
    clients map[uuid.UUID]map[*client]struct{}
}

func NewHub() *Hub {
    return &Hub{clients: make(map[uuid.UUID]map[*client]struct{})}
}

func (h *Hub) Register(conn *websocket.Conn, userID uuid.UUID) *Connection {
    c := &client{
        conn:   conn,
        userID: userID,
        send:   make(chan []byte, 64),
    }

    h.mu.Lock()
    set, ok := h.clients[userID]
    if !ok {
        set = make(map[*client]struct{})
        h.clients[userID] = set
    }
    set[c] = struct{}{}
    h.mu.Unlock()

    return &Connection{hub: h, c: c}
}

func (h *Hub) HasConnections(userID uuid.UUID) bool {
    h.mu.RLock()
    defer h.mu.RUnlock()
    set, ok := h.clients[userID]
    return ok && len(set) > 0
}

func (h *Hub) unregister(c *client) {
    h.mu.Lock()
    defer h.mu.Unlock()

    set, ok := h.clients[c.userID]
    if !ok {
        return
    }

    delete(set, c)
    if len(set) == 0 {
        delete(h.clients, c.userID)
    }
    close(c.send)
}

func (h *Hub) SendToUser(userID uuid.UUID, event Event) {
    payload, err := json.Marshal(event)
    if err != nil {
        log.Printf("marshal realtime event: %v", err)
        return
    }

    h.mu.RLock()
    defer h.mu.RUnlock()

    set := h.clients[userID]
    for c := range set {
        select {
        case c.send <- payload:
        default:
        }
    }
}

func (h *Hub) BroadcastToUsers(userIDs []uuid.UUID, event Event) {
    for _, uid := range userIDs {
        h.SendToUser(uid, event)
    }
}

type Connection struct {
    hub *Hub
    c   *client
}

func (c *Connection) Run(
    onMessage func(map[string]any),
    onClose func(),
) {
    done := make(chan struct{})

    go func() {
        defer close(done)
        for msg := range c.c.send {
            c.c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
            if err := c.c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
                return
            }
        }
    }()

    c.c.conn.SetReadLimit(2 * 1024 * 1024)
    _ = c.c.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
    c.c.conn.SetPongHandler(func(string) error {
        _ = c.c.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
        return nil
    })

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    go func() {
        for range ticker.C {
            c.c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
            if err := c.c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                return
            }
        }
    }()

    for {
        _, message, err := c.c.conn.ReadMessage()
        if err != nil {
            break
        }
        var raw map[string]any
        if unmarshalErr := json.Unmarshal(message, &raw); unmarshalErr != nil {
            continue
        }
        onMessage(raw)
    }

    c.hub.unregister(c.c)
    _ = c.c.conn.Close()
    onClose()
    <-done
}
