package core

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestWALReadersDoNotWaitForWriterAndSeeCommittedRevocation(t *testing.T) {
	f := newFixture(t)
	tx, err := f.c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE members SET can_send=0 WHERE chat_id='chat' AND user_id='alice'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, send, _, err := f.c.Member(ctx, "alice", "chat")
	if err != nil || !send {
		t.Fatalf("reader must see previous committed snapshot while writer held: send=%v err=%v", send, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, send, _, err = f.c.Member(ctx, "alice", "chat")
	if err != nil || send {
		t.Fatalf("new read must see committed permission change: send=%v err=%v", send, err)
	}
}

func TestSQLiteSafetyPragmasApplyToEveryPhysicalReader(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var held []*sql.Conn
	defer func() {
		for _, conn := range held {
			conn.Close()
		}
	}()
	for i := 0; i < min(3, f.c.ReadStats().MaxOpenConnections); i++ {
		conn, err := f.c.reader().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
		for pragma, want := range map[string]int{"foreign_keys": 1, "busy_timeout": 5000, "synchronous": 2, "secure_delete": 1, "query_only": 1} {
			var got int
			if err = conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("connection %d %s=%d want=%d err=%v", i, pragma, got, want, err)
			}
		}
		if _, err = conn.ExecContext(ctx, `UPDATE users SET disabled=1 WHERE id='alice'`); err == nil {
			t.Fatal("read-only connection allowed mutation")
		}
	}
	var sync int
	if err := f.c.DB.QueryRow(`PRAGMA synchronous`).Scan(&sync); err != nil || sync != 2 {
		t.Fatalf("writer FULL durability lost: %d %v", sync, err)
	}
}
