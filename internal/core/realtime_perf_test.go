package core

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestNotificationsCoalesceAndUnsubscribe(t *testing.T) {
	c := &Core{}
	conn := &connection{wake: make(chan struct{}, 1)}
	c.subscribe(conn, "a", true)
	c.subscribe(conn, "b", true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.Wake("a")
				c.Wake("b")
			}
		}()
	}
	wg.Wait()
	if len(conn.wake) != 1 {
		t.Fatal("notifications did not coalesce")
	}
	c.subscribe(conn, "a", false)
	c.Wake("a")
	<-conn.wake
	pending := conn.takePending()
	if len(pending) != 1 {
		t.Fatalf("unsubscribe retained pending chats: %v", pending)
	}
	if _, ok := pending["b"]; !ok {
		t.Fatal("coalescing lost another subscribed chat")
	}
	c.Wake("b")
	select {
	case <-conn.wake:
	default:
		t.Fatal("notification after drain was lost")
	}
}

func TestWebsocketPagedReplayAndMissingNotification(t *testing.T) {
	f := newFixture(t)
	// Journal-only events exercise replay scheduling without encryption costs.
	appendEvents := func(first, last int) {
		t.Helper()
		tx, err := f.c.DB.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for seq := first; seq <= last; seq++ {
			if _, err = tx.Exec(`INSERT INTO events(chat_id,seq,kind,message_id,data,created_at) VALUES('chat',?,'test','',NULL,?)`, seq, time.Now().Unix()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = tx.Exec(`UPDATE chats SET seq=? WHERE id='chat'`, last); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	appendEvents(1, 1105)
	data := f.require(t, "POST", "/v1/ws-tickets", nil, 201, false)
	var result struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/v1/ws?ticket=" + result.Ticket
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = conn.WriteJSON(wsCommand{Type: "subscribe", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 1105; seq++ {
		var event Event
		if err = conn.ReadJSON(&event); err != nil || event.Seq != seq {
			t.Fatalf("ordered replay at %d: %#v %v", seq, event, err)
		}
	}
	appendEvents(1106, 1106) // Deliberately omit Wake: polling must recover it.
	var event Event
	if err = conn.ReadJSON(&event); err != nil || event.Seq != 1106 {
		t.Fatalf("missing notification recovery: %#v %v", event, err)
	}
}
