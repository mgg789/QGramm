package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSharedReplayCoalescesHeadsAndPrunes(t *testing.T) {
	f := newFixture(t)
	connections := make([]*connection, 2000)
	for i := range connections {
		connections[i] = &connection{wake: make(chan struct{}, 1)}
		f.c.subscribe(connections[i], "chat", true)
	}
	f.c.pollReplayHeads()
	for _, conn := range connections {
		<-conn.wake
		conn.takePending()
	}
	f.c.pollReplayHeads()
	for _, conn := range connections {
		if len(conn.wake) != 0 {
			t.Fatal("unchanged head woke idle subscriber")
		}
	}
	// A commit without Wake still advances the authoritative journal head.
	if _, err := f.c.DB.Exec(`UPDATE chats SET seq=1 WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	f.c.pollReplayHeads()
	for _, conn := range connections {
		if len(conn.wake) != 1 {
			t.Fatal("changed head did not wake subscriber")
		}
		f.c.subscribe(conn, "chat", false)
	}
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	defer f.c.replayMu.Unlock()
	if len(f.c.replayHeads) != 0 {
		t.Fatal("head observations retained unsubscribed chats")
	}
}

func TestSharedReplayReconnectFlushesUnchangedHead(t *testing.T) {
	f := newFixture(t)
	tx, err := f.c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO events(chat_id,seq,kind,created_at) VALUES('chat',1,'test',?)`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE chats SET seq=1 WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Keep an observation alive while real clients reconnect. Their initial
	// flush must use the supplied cursor even though the shared head is stable.
	sentinel := &connection{wake: make(chan struct{}, 1)}
	f.c.subscribe(sentinel, "chat", true)
	defer f.c.subscribe(sentinel, "chat", false)
	f.c.pollReplayHeads()
	for attempt := 0; attempt < 2; attempt++ {
		data := f.require(t, "POST", "/v1/ws-tickets", nil, 201, false)
		var ticket struct {
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal(data, &ticket); err != nil {
			t.Fatal(err)
		}
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/ws?ticket="+ticket.Ticket, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if err = conn.WriteJSON(wsCommand{Type: "subscribe", ChatID: "chat"}); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		var event Event
		err = conn.ReadJSON(&event)
		conn.Close()
		if err != nil || event.Seq != 1 {
			t.Fatalf("reconnect %d missed existing event: %#v %v", attempt, event, err)
		}
	}
}

func TestSharedReplayBatchesMoreThanSQLiteVariableLimit(t *testing.T) {
	f := newFixture(t)
	tx, err := f.c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1200; i++ {
		// Fixed, unique textual IDs keep this test independent of user/device setup.
		id := string(rune(0x1000 + i))
		if _, err = tx.Exec(`INSERT INTO chats(id,kind,mode,seq,created_at) VALUES(?,'direct','basic',1,0)`, id); err != nil {
			t.Fatal(err)
		}
		f.c.subscribe(&connection{wake: make(chan struct{}, 1)}, id, true)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.c.pollReplayHeads()
	f.c.replayMu.Lock()
	defer f.c.replayMu.Unlock()
	if len(f.c.replayHeads) != 1200 {
		t.Fatalf("batched poll observed %d heads", len(f.c.replayHeads))
	}
}

func TestSharedReplayDoesNotBypassRevokedMembership(t *testing.T) {
	f := newFixture(t)
	conn := &connection{wake: make(chan struct{}, 1)}
	f.c.subscribe(conn, "chat", true)
	f.c.pollReplayHeads()
	<-conn.wake
	conn.takePending()
	if _, err := f.c.DB.Exec(`UPDATE members SET active=0 WHERE user_id='alice' AND chat_id='chat'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.DB.Exec(`UPDATE chats SET seq=1 WHERE id='chat'`); err != nil {
		t.Fatal(err)
	}
	f.c.pollReplayHeads()
	if len(conn.wake) != 1 {
		t.Fatal("head change not signaled")
	}
	if _, err := f.c.Events(context.Background(), f.id, "chat", 0, 100); err == nil {
		t.Fatal("replay bypassed revoked membership")
	}
}
