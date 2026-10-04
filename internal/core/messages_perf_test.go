package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestMinimalAcceptanceReceiptAndRetry(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "minimal-op", "receipt must not contain plaintext or ciphertext")
	projections := 0
	f.c.Project = append(f.c.Project, func(_ context.Context, _ Identity, _ *Message) error {
		projections++
		return nil
	})
	request := func(prefer string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/chats/chat/messages", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+f.token(f.id, f.cfg.Security.Issuer))
		r.Header.Set("Prefer", prefer)
		w := httptest.NewRecorder()
		f.c.Handler().ServeHTTP(w, r)
		return w
	}
	first := request("return=minimal")
	if first.Code != 201 || first.Header().Get("Preference-Applied") != "return=minimal" {
		t.Fatalf("minimal acceptance: %d %s", first.Code, first.Body)
	}
	var result struct {
		Status  string         `json:"status"`
		Receipt map[string]any `json:"receipt"`
		Message *Message       `json:"message"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "accepted" || result.Message != nil || len(result.Receipt) != 4 || result.Receipt["message_id"] == "" || result.Receipt["operation_id"] != in.OperationID || result.Receipt["seq"] != float64(1) {
		t.Fatalf("unexpected receipt: %s", first.Body)
	}
	retry := request("respond-async, return=minimal; custom=1")
	if retry.Code != 201 || retry.Body.String() != first.Body.String() {
		t.Fatalf("retry changed receipt: %d %s", retry.Code, retry.Body)
	}
	if projections != 0 {
		t.Fatalf("minimal acceptance projected message content %d times", projections)
	}
	full := request("")
	if full.Code != 201 || full.Header().Get("Preference-Applied") != "" {
		t.Fatalf("default response: %d %s", full.Code, full.Body)
	}
	if err := json.Unmarshal(full.Body.Bytes(), &result); err != nil || result.Message == nil || result.Message.Envelope == nil {
		t.Fatalf("default projection missing: %s", full.Body)
	}
	if projections != 1 {
		t.Fatalf("default response skipped projection: %d", projections)
	}
	if _, err := f.c.DB.Exec(`UPDATE members SET active=0 WHERE chat_id='chat' AND user_id='alice'`); err != nil {
		t.Fatal(err)
	}
	if got := request("return=minimal"); got.Code != 404 {
		t.Fatalf("receipt retry bypassed membership: %d %s", got.Code, got.Body)
	}
}

func TestEventsLimitBounded(t *testing.T) {
	f := newFixture(t)
	for _, limit := range []int{-1, 0, 201, 100_000} {
		if _, err := f.c.Events(context.Background(), f.id, "chat", 0, limit); err == nil {
			t.Fatalf("accepted invalid event limit %d", limit)
		}
	}
}

func TestMinimalBatchPreservesPartialResultsAndDedup(t *testing.T) {
	f := newFixture(t)
	in := map[string]any{"messages": []MessageInput{f.input(t, "batch-valid", "batched content"), {OperationID: "invalid", MLS: "wrong mode"}}}
	request := func() *httptest.ResponseRecorder {
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		r.SetPathValue("chat", "chat")
		r.Header.Set("Prefer", "return=minimal")
		w := httptest.NewRecorder()
		f.c.batch(w, r, f.id)
		return w
	}
	first := request()
	var results []map[string]any
	if first.Code != 207 || first.Header().Get("Preference-Applied") != "return=minimal" {
		t.Fatalf("batch: %d %s", first.Code, first.Body)
	}
	if err := json.Unmarshal(first.Body.Bytes(), &results); err != nil || len(results) != 2 || results[0]["status"] != float64(201) || results[0]["receipt"] == nil || results[0]["message"] != nil || results[1]["status"] != float64(400) {
		t.Fatalf("batch partial results: %s", first.Body)
	}
	if second := request(); second.Body.String() != first.Body.String() {
		t.Fatalf("batch retry changed receipt: %s", second.Body)
	}
	var count int
	if err := f.c.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("batch dedup count=%d err=%v", count, err)
	}
}

func TestBatchedEventProjectionUsesCurrentMessageAndHooks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.c.Send(ctx, f.id, "chat", f.input(t, "first", "first content"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.c.Send(ctx, f.id, "chat", f.input(t, "second", "second content"))
	if err != nil {
		t.Fatal(err)
	}
	// Replay an earlier creation and a later deletion as current tombstone state.
	tx, err := f.c.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE messages SET deleted=1,payload=X'',metadata='{}',revision=2 WHERE id=?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Append(ctx, tx, "chat", "message.deleted", first.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	projected := map[string]int{}
	f.c.Project = append(f.c.Project, func(ctx context.Context, id Identity, message *Message) error {
		projected[message.ID]++
		// Projection hooks retain storage access; holding rows here would deadlock
		// a single-connection reader pool.
		var present bool
		return f.c.reader().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=?)`, message.ID).Scan(&present)
	})
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id := Identity{"bob", "bob-phone"}
	events, err := f.c.Events(ctx, id, "chat", 0, 100)
	if err != nil || len(events) != 3 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if !reflect.DeepEqual(projected, map[string]int{first.ID: 1, second.ID: 1}) {
		t.Fatalf("projection work duplicated: %v", projected)
	}
	for _, event := range events {
		message := event.Data.(Message)
		if message.ID == first.ID && (!message.Deleted || message.Revision != 2 || message.Envelope != nil || message.MLS != "" || len(message.Attachments) != 0) {
			t.Fatalf("deleted content reappeared: %+v", message)
		}
		if message.ID == second.ID && message.Envelope == nil {
			t.Fatal("surviving message lost encrypted envelope")
		}
	}
	if _, err = f.c.DB.Exec(`UPDATE members SET active=0 WHERE chat_id='chat' AND user_id='bob'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Events(ctx, id, "chat", 0, 100); err == nil {
		t.Fatal("batched projection bypassed membership")
	}
}
