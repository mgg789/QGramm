package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/mgg789/QGramm/internal/cryptoenc"
)

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
