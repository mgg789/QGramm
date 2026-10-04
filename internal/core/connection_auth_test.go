package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSharedDeviceValidationRevocationAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "storage_failure"}[failure], func(t *testing.T) {
			f := newFixture(t)
			data := f.require(t, "POST", "/v1/ws-tickets", nil, 201, false)
			var ticket struct {
				Ticket string `json:"ticket"`
			}
			if err := json.Unmarshal(data, &ticket); err != nil {
				t.Fatal(err)
			}
			ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/ws?ticket="+ticket.Ticket, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ws.Close()
			var registered *connection
			deadline := time.Now().Add(time.Second)
			for registered == nil && time.Now().Before(deadline) {
				f.c.mu.Lock()
				for conn := range f.c.connections[f.id.DeviceID] {
					registered = conn
				}
				f.c.mu.Unlock()
				if registered == nil {
					time.Sleep(time.Millisecond)
				}
			}
			if registered == nil {
				t.Fatal("socket not registered")
			}
			f.c.validateConnections()
			select {
			case <-registered.done:
				t.Fatal("valid device disconnected")
			default:
			}
			if failure {
				if err = f.c.readDB.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				// Bypass management deliberately: the shared fallback must catch it.
				if _, err = f.c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id=?`, f.id.DeviceID); err != nil {
					t.Fatal(err)
				}
			}
			f.c.validateConnections()
			select {
			case <-registered.done:
			default:
				t.Fatal("invalid device still connected")
			}
			ws.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, err = ws.ReadMessage(); err == nil {
				t.Fatal("socket remained readable after revocation")
			}
		})
	}
}
