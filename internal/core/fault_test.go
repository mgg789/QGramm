package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDiskFullDoesNotPublishPartialOperation(t *testing.T) {
	f := newFixture(t)
	var pages int
	if err := f.c.DB.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.DB.Exec(`PRAGMA max_page_count=` + jsonNumber(pages)); err != nil {
		t.Fatal(err)
	}
	// A large record must allocate pages. SQLite's real SQLITE_FULL path rolls
	// back the message/event/idempotency transaction, just as an exhausted disk.
	in := f.input(t, "disk-full", strings.Repeat("x", 32000))
	if _, err := f.c.Send(context.Background(), f.id, "chat", in); err == nil {
		t.Fatal("full database accepted write")
	}
	for _, table := range []string{"messages", "events", "operations"} {
		var count int
		if err := f.c.DB.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial %s count=%d err=%v", table, count, err)
		}
	}
	if _, err := f.c.DB.Exec(`PRAGMA max_page_count=1000000`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.Send(context.Background(), f.id, "chat", in); err != nil {
		t.Fatal("retry after disk recovery", err)
	}
}
func jsonNumber(n int) string { raw, _ := json.Marshal(n); return string(raw) }

func TestBatchLostACKRetriesOnlyOneEventPerItem(t *testing.T) {
	f := newFixture(t)
	input := []MessageInput{f.input(t, "batch-1", "one"), f.input(t, "batch-2", "two")}
	first := f.require(t, "POST", "/v1/chats/chat/messages/batch", map[string]any{"messages": input}, 207, false)
	// Simulate a lost response: resend the exact encrypted batch and IDs.
	second := f.require(t, "POST", "/v1/chats/chat/messages/batch", map[string]any{"messages": input}, 207, false)
	var a, b any
	if json.Unmarshal(first, &a) != nil || json.Unmarshal(second, &b) != nil {
		t.Fatal("batch response invalid")
	}
	events, err := f.c.Events(context.Background(), Identity{"bob", "bob-phone"}, "chat", 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatal("batch duplicated durable events", len(events), err)
	}
}
