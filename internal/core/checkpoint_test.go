package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPassiveCheckpointPreservesFullAndDoesNotWaitForWriter(t *testing.T) {
	f := newFixture(t)
	f.c.Config.Storage.CheckpointIntervalMS = 100
	f.c.Config.Storage.CheckpointWALBytes = 65536
	if err := f.c.startCheckpointer(); err != nil {
		t.Fatal(err)
	}
	for pragma, want := range map[string]int{"synchronous": 2, "busy_timeout": 0, "wal_autocheckpoint": 1000} {
		var got int
		if err := f.c.checkpoint.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("checkpoint %s=%d want%d: %v", pragma, got, want, err)
		}
	}
	// Force a new physical connection; safety must come from the DSN, rather
	// than a one-time PRAGMA applied to the connection opened at startup.
	f.c.checkpoint.db.SetMaxIdleConns(0)
	var reconnected int
	if err := f.c.checkpoint.db.QueryRow("PRAGMA busy_timeout").Scan(&reconnected); err != nil || reconnected != 0 {
		t.Fatalf("reconnected busy_timeout=%d: %v", reconnected, err)
	}
	f.c.checkpoint.db.SetMaxIdleConns(1)
	var full, automatic int
	if err := f.c.DB.QueryRow("PRAGMA synchronous").Scan(&full); err != nil || full != 2 {
		t.Fatalf("writer FULL=%d: %v", full, err)
	}
	if err := f.c.DB.QueryRow("PRAGMA wal_autocheckpoint").Scan(&automatic); err != nil || automatic != 1000 {
		t.Fatalf("writer auto checkpoint=%d: %v", automatic, err)
	}
	tx, err := f.c.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE users SET disabled=1 WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	busy, _, _, err := f.c.passiveCheckpoint(ctx, f.c.checkpoint.db)
	if err != nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("PASSIVE waited for writer: busy=%d err=%v elapsed=%v", busy, err, time.Since(started))
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = f.c.passiveCheckpoint(ctx, f.c.checkpoint.db); err != nil {
		t.Fatal(err)
	}
	f.c.cancel()
	select {
	case <-f.c.checkpoint.done:
	case <-time.After(time.Second):
		t.Fatal("checkpoint worker did not stop")
	}
}

func TestPassiveCheckpointPinnedReaderPreservesNewCommit(t *testing.T) {
	f := newFixture(t)
	f.c.Config.Storage.CheckpointIntervalMS = 100
	if err := f.c.startCheckpointer(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, _, err := f.c.passiveCheckpoint(ctx, f.c.checkpoint.db); err != nil {
		t.Fatal(err)
	}
	tx, err := f.c.reader().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var before int
	if err = tx.QueryRow("SELECT COUNT(*) FROM users").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.DB.Exec("INSERT INTO users(id) VALUES('checkpoint-new')"); err != nil {
		t.Fatal(err)
	}
	_, log, done, err := f.c.passiveCheckpoint(ctx, f.c.checkpoint.db)
	if err != nil || log <= done {
		t.Fatalf("expected pinned reader to defer frames: log=%d done=%d err=%v", log, done, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, log, done, err = f.c.passiveCheckpoint(ctx, f.c.checkpoint.db)
	if err != nil || log != done {
		t.Fatalf("checkpoint did not catch up: log=%d done=%d err=%v", log, done, err)
	}
	var after int
	if err = f.c.reader().QueryRow("SELECT COUNT(*) FROM users").Scan(&after); err != nil || after != before+1 {
		t.Fatalf("commit lost: before=%d after=%d err=%v", before, after, err)
	}
}

func TestCheckpointerDisabledHasNoResources(t *testing.T) {
	f := newFixture(t)
	if f.c.checkpoint != nil {
		t.Fatal("disabled checkpoint created worker/pool")
	}
}

func TestConfiguredCheckpointerOpenAndShutdown(t *testing.T) {
	f := newFixture(t)
	cfg := f.cfg
	cfg.Storage.Path = filepath.Join(t.TempDir(), "worker.db")
	cfg.Storage.CheckpointIntervalMS = 100
	cfg.Storage.CheckpointWALBytes = 65536
	c, err := Open(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.checkpoint == nil {
		c.Close()
		t.Fatal("configured Open omitted checkpoint worker")
	}
	done := c.checkpoint.done
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Close returned before worker stopped")
	}
	if err = c.checkpoint.db.Ping(); err == nil {
		t.Fatal("checkpoint connection remained open")
	}
}
