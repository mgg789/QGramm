package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

func TestHistoryProjectionOrderedBoundedAndFresh(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := f.c.Send(ctx, f.id, "chat", f.input(t, "first-history", "old"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.c.Send(ctx, f.id, "chat", f.input(t, "second-history", "second"))
	if err != nil {
		t.Fatal(err)
	}
	third, err := f.c.Send(ctx, f.id, "chat", f.input(t, "third-history", "third"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.DB.Exec(`UPDATE members SET joined_seq=? WHERE chat_id='chat' AND user_id='bob'`, second.Seq); err != nil {
		t.Fatal(err)
	}
	f.c.reader().SetMaxOpenConns(1)
	projected := make(map[string]int)
	f.c.Project = append(f.c.Project, func(ctx context.Context, id Identity, m *Message) error {
		projected[m.ID]++
		var active bool
		if err := f.c.readQueryRow(ctx, `SELECT active FROM members WHERE chat_id=? AND user_id=?`, m.ChatID, id.UserID).Scan(&active); err != nil {
			return err
		}
		if m.ID == second.ID {
			m.Deleted = true
		}
		return nil
	})
	request := func(limit string, identity Identity) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/?after=0&limit="+limit, nil).WithContext(ctx)
		r.SetPathValue("chat", "chat")
		w := httptest.NewRecorder()
		f.c.history(w, r, identity)
		return w
	}
	id := Identity{"bob", "bob-phone"}
	w := request("2", id)
	var messages []Message
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &messages) != nil || len(messages) != 2 || messages[0].ID != second.ID || messages[1].ID != third.ID || !messages[0].Deleted || messages[0].Envelope != nil || messages[1].Envelope == nil || projected[first.ID] != 0 || projected[second.ID] != 1 || projected[third.ID] != 1 {
		t.Fatalf("history order/ACL/tombstone/hooks changed: status=%d messages=%+v calls=%v", w.Code, messages, projected)
	}
	plain, err := f.bob.OpenEnvelope(*messages[1].Envelope, cryptoenc.Binding("chat", "bob", "bob-phone", third.ID))
	if err != nil || string(plain) != "third" {
		t.Fatal("history envelope invalid", err)
	}
	w = request("1", id)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &messages) != nil || len(messages) != 1 || messages[0].ID != second.ID {
		t.Fatal("history limit ignored", w.Code)
	}
	emptyRequest := httptest.NewRequest(http.MethodGet, "/?after=9999", nil).WithContext(ctx)
	emptyRequest.SetPathValue("chat", "chat")
	w = httptest.NewRecorder()
	f.c.history(w, emptyRequest, id)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &messages) != nil || messages == nil || len(messages) != 0 {
		t.Fatal("authorized empty history changed", w.Code)
	}
	if w = request("2", Identity{"bob", "alice-phone"}); w.Code != 403 {
		t.Fatal("foreign device accessed history", w.Code)
	}
	if _, err = f.c.DB.Exec(`UPDATE devices SET revoked=1 WHERE id='bob-phone'`); err != nil {
		t.Fatal(err)
	}
	if w = request("2", id); w.Code != 403 {
		t.Fatal("revoked device accessed history", w.Code)
	}
	w = httptest.NewRecorder()
	f.c.history(w, emptyRequest, id)
	if w.Code != 403 {
		t.Fatal("revoked device accessed empty history", w.Code)
	}
}

func TestJoinedEventProjectionKeepsNonMessageEventsAndRejectsCrossChatReference(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "event-join", "hello"))
	if err != nil {
		t.Fatal(err)
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
	events, err := f.c.Events(ctx, Identity{"bob", "bob-phone"}, "chat", 0, 200)
	if err != nil || len(events) != 2 || events[0].Data.(Message).ID != m.ID || events[1].Type != "receipt.updated" || events[1].Data.(map[string]any)["read"] != float64(1) {
		t.Fatal("mixed event page changed", events, err)
	}
	if _, err = f.c.DB.Exec(`UPDATE members SET joined_seq=? WHERE chat_id='chat' AND user_id='bob'`, m.Seq+1); err != nil {
		t.Fatal(err)
	}
	events, err = f.c.Events(ctx, Identity{"bob", "bob-phone"}, "chat", 0, 200)
	if err != nil || len(events) != 1 || events[0].Type != "receipt.updated" {
		t.Fatal("join-time filtering changed", events, err)
	}
	// A corrupt/foreign-chat reference must fail closed, not project a message
	// merely because the recipient happens to belong to both chats.
	if _, err = f.c.DB.Exec(`UPDATE members SET joined_seq=1 WHERE chat_id='chat' AND user_id='bob'`); err != nil {
		t.Fatal(err)
	}
	f.require(t, "POST", "/management/v1/chats/direct", map[string]any{"id": "foreign", "members": []string{"alice", "bob"}}, 201, true)
	if _, err = f.c.DB.Exec(`UPDATE messages SET chat_id='foreign' WHERE id=?`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Events(ctx, Identity{"bob", "bob-phone"}, "chat", 0, 200); err == nil {
		t.Fatal("cross-chat event reference accepted")
	}
}

func TestProjectionRechecksActiveRecipientAndEmptyReplay(t *testing.T) {
	for _, change := range []string{
		`UPDATE devices SET revoked=1 WHERE id='bob-phone'`,
		`UPDATE users SET disabled=1 WHERE id='bob'`,
		`UPDATE members SET active=0 WHERE user_id='bob' AND chat_id='chat'`,
	} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "projection", "hello"))
			if err != nil {
				t.Fatal(err)
			}
			id := Identity{"bob", "bob-phone"}
			if _, err = f.c.ViewMessage(ctx, id, m.ID); err != nil {
				t.Fatal(err)
			}
			if events, err := f.c.Events(ctx, id, "chat", m.Seq, 200); err != nil || len(events) != 0 {
				t.Fatal(events, err)
			}
			if _, err = f.c.DB.Exec(change); err != nil {
				t.Fatal(err)
			}
			if _, err = f.c.ViewMessage(ctx, id, m.ID); err == nil {
				t.Fatal("revoked projection accepted")
			}
			if _, err = f.c.Events(ctx, id, "chat", m.Seq, 200); err == nil {
				t.Fatal("empty replay bypassed revocation")
			}
		})
	}
}

func TestProjectionRejectsForeignDeviceAndUsesRotatedKey(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "rotation", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.ViewMessage(ctx, Identity{"bob", "alice-phone"}, m.ID); err == nil {
		t.Fatal("foreign device accepted")
	}
	replacement, err := cryptoenc.New(bytes.Repeat([]byte{8}, 32), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.DB.Exec(`UPDATE devices SET public_key=? WHERE id='bob-phone'`, replacement.PublicKey()); err != nil {
		t.Fatal(err)
	}
	view, err := f.c.ViewMessage(ctx, Identity{"bob", "bob-phone"}, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := cryptoenc.Binding("chat", "bob", "bob-phone", m.ID)
	plain, err := replacement.OpenEnvelope(*view.Envelope, binding)
	if err != nil || string(plain) != "hello" {
		t.Fatal("rotated key not used", err)
	}
	if _, err = f.bob.OpenEnvelope(*view.Envelope, binding); err == nil {
		t.Fatal("retired recipient key used")
	}
}

func TestProjectionClosesRowsDeduplicatesHooksAndKeepsTombstone(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "hooks", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	f.c.reader().SetMaxOpenConns(1)
	calls := 0
	f.c.Project = append(f.c.Project, func(ctx context.Context, id Identity, m *Message) error {
		calls++
		var active bool
		if err := f.c.readQueryRow(ctx, `SELECT active FROM members WHERE chat_id=? AND user_id=?`, m.ChatID, id.UserID).Scan(&active); err != nil {
			return err
		}
		m.Deleted = true
		return nil
	})
	messages, err := f.c.viewMessages(ctx, Identity{"bob", "bob-phone"}, []string{m.ID, m.ID})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(messages) != 1 || !messages[m.ID].Deleted || messages[m.ID].Envelope != nil {
		t.Fatal("projection hook/tombstone behavior changed", calls, messages)
	}
	if _, err = f.c.ViewMessage(ctx, Identity{"bob", "bob-phone"}, m.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("single projection skipped hook")
	}
	if _, err = f.c.DB.Exec(`UPDATE messages SET deleted=1,payload=X'' WHERE id=?`, m.ID); err != nil {
		t.Fatal(err)
	}
	f.c.Project = nil
	deleted, err := f.c.ViewMessage(ctx, Identity{"bob", "bob-phone"}, m.ID)
	if err != nil || !deleted.Deleted || deleted.Envelope != nil || deleted.MLS != "" || len(deleted.Attachments) != 0 {
		t.Fatal("stored tombstone changed", deleted, err)
	}
}

func TestSendDigestPreservesOriginalOperationBytes(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "hash", "hello")
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(append([]byte("chat\x00"), raw...))
	m, err := f.c.Send(context.Background(), f.id, "chat", in)
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err = f.c.DB.QueryRow(`SELECT hash FROM operations WHERE device_id=? AND operation_id=?`, f.id.DeviceID, in.OperationID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != hex.EncodeToString(expected[:]) {
		t.Fatal("operation hash changed")
	}
	if in.Envelope == nil {
		t.Fatal("caller input changed")
	}
	var metadata []byte
	if err = f.c.DB.QueryRow(`SELECT metadata FROM messages WHERE id=?`, m.ID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(map[string]any{"attachments": in.Attachments, "forward_from": in.ForwardFrom, "reply_to": in.ReplyTo})
	if !bytes.Equal(metadata, old) {
		t.Fatalf("metadata bytes changed: %s", metadata)
	}
}
