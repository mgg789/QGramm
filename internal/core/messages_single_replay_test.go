package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Commit another event while projecting the first one. The one-event query
// returns its validated prefix; the next replay must retain the new head.
func TestSingleEventReplayConcurrentAppendReturnsContiguousPrefix(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := f.c.Send(ctx, f.id, "chat", f.input(t, "prefix-first", "first"))
	if err != nil {
		t.Fatal(err)
	}
	input := f.input(t, "prefix-second", "second")
	projecting := make(chan struct{})
	type result struct {
		message Message
		err     error
	}
	committed := make(chan result, 1)
	go func() {
		select {
		case <-projecting:
			message, err := f.c.sendMessage(ctx, f.id, "chat", input, true)
			committed <- result{message, err}
		case <-ctx.Done():
			committed <- result{err: ctx.Err()}
		}
	}()
	var second Message
	f.c.Project = append(f.c.Project, func(ctx context.Context, _ Identity, m *Message) error {
		if m.ID != first.ID {
			return nil
		}
		close(projecting)
		select {
		case outcome := <-committed:
			second = outcome.message
			return outcome.err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	id := Identity{"bob", "bob-phone"}
	events, err := f.c.Events(ctx, id, "chat", 0, 200)
	if err != nil || len(events) != 1 || events[0].Seq != first.Seq || second.Seq != first.Seq+1 {
		t.Fatal("concurrent append changed observed prefix", events, second.Seq, err)
	}
	events, err = f.c.Events(ctx, id, "chat", events[0].Seq, 200)
	if err != nil || len(events) != 1 || events[0].Seq != second.Seq || events[0].MessageID != second.ID {
		t.Fatal("concurrent append lost next event", events, err)
	}
	if events, err = f.c.Events(ctx, id, "chat", second.Seq, 200); err != nil || len(events) != 0 {
		t.Fatal("replay duplicated accepted event", events, err)
	}
}

func TestSingleEventWebsocketWakeRetainsConcurrentAppend(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := f.c.Send(ctx, f.id, "chat", f.input(t, "wake-first", "first"))
	if err != nil {
		t.Fatal(err)
	}
	input := f.input(t, "wake-second", "second")
	projecting := make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		select {
		case <-projecting:
			_, err := f.c.sendMessage(ctx, f.id, "chat", input, true)
			committed <- err
		case <-ctx.Done():
			committed <- ctx.Err()
		}
	}()
	f.c.Project = append(f.c.Project, func(ctx context.Context, _ Identity, m *Message) error {
		if m.ID != first.ID {
			return nil
		}
		close(projecting)
		select {
		case err := <-committed:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	data := f.require(t, "POST", "/v1/ws-tickets", nil, 201, false)
	var ticket struct{ Ticket string }
	if err := json.Unmarshal(data, &ticket); err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/ws?ticket="+ticket.Ticket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err = conn.WriteJSON(wsCommand{Type: "subscribe", ChatID: "chat"}); err != nil {
		t.Fatal(err)
	}
	for seq := int64(1); seq <= 2; seq++ {
		var event Event
		if err := conn.ReadJSON(&event); err != nil || event.Seq != seq || event.Type != "message.created" {
			t.Fatalf("concurrent wake lost ordered event %d: %+v %v", seq, event, err)
		}
	}
}

func TestSingleEventLimitOneRetainsBacklog(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, op := range []string{"limited-first", "limited-second"} {
		if _, err := f.c.Send(ctx, f.id, "chat", f.input(t, op, op)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := f.c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = f.c.Append(ctx, tx, "chat", "receipt.updated", "", map[string]any{"read": 1}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for after := int64(0); after < 3; after++ {
		events, err := f.c.Events(ctx, Identity{"bob", "bob-phone"}, "chat", after, 1)
		if err != nil || len(events) != 1 || events[0].Seq != after+1 {
			t.Fatal("limit-one replay skipped backlog", after, events, err)
		}
		if after == 2 && events[0].Data.(map[string]any)["read"] != float64(1) {
			t.Fatal("non-message event projection changed", events)
		}
	}
}

func TestSingleEventLimitOneRetainsNearestAvailableInteriorSequence(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, op := range []string{"gap-first", "gap-second", "gap-third"} {
		if _, err := f.c.Send(ctx, f.id, "chat", f.input(t, op, op)); err != nil {
			t.Fatal(err)
		}
	}
	// Preserve the existing replay semantics for a damaged interior journal:
	// MIN(seq) remains valid and the nearest available event must be returned.
	// A point lookup for after+1 would silently hide the remaining event.
	if _, err := f.c.DB.Exec(`DELETE FROM events WHERE chat_id='chat' AND seq=2`); err != nil {
		t.Fatal(err)
	}
	events, err := f.c.Events(ctx, Identity{"bob", "bob-phone"}, "chat", 1, 1)
	if err != nil || len(events) != 1 || events[0].Seq != 3 {
		t.Fatal("limit-one replay hid nearest available event", events, err)
	}
}
