package core

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"time"
)

// Optional PASSIVE checkpoints use a separate connection and do not wait for
// writer locks; their I/O can still compete with commits. SQLite auto-checkpoint remains
// enabled as a safety fallback; this worker never truncates the WAL or weakens
// synchronous=FULL. A pinned reader can prevent progress, so WAL size is a
// trigger, not a guarantee of a storage bound.
type walCheckpointer struct {
	db   *sql.DB
	done chan struct{}
}

func (c *Core) startCheckpointer() error {
	if c.Config.Storage.CheckpointIntervalMS == 0 {
		return nil
	}
	dsn, err := sqliteDSN(c.Config.Storage.Path, false)
	if err != nil {
		return err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	q := u.Query()
	// PASSIVE does not invoke the busy handler. Also forbid waits in connection
	// setup or subsequent accidental lock contention on this dedicated pool.
	pragmas := q["_pragma"]
	q.Del("_pragma")
	for _, pragma := range pragmas {
		if pragma != "busy_timeout(5000)" {
			q.Add("_pragma", pragma)
		}
	}
	q.Add("_pragma", "busy_timeout(0)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.PingContext(c.Context); err != nil {
		db.Close()
		return err
	}
	w := &walCheckpointer{db: db, done: make(chan struct{})}
	c.checkpoint = w
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(time.Duration(c.Config.Storage.CheckpointIntervalMS) * time.Millisecond)
		defer ticker.Stop()
		threshold := c.Config.Storage.CheckpointWALBytes
		if threshold == 0 {
			threshold = 4 << 20
		}
		var completedSize int64
		var completedModified time.Time
		for {
			select {
			case <-c.Context.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(c.Config.Storage.Path + "-wal")
				if err != nil || info.Size() < threshold {
					continue
				}
				// PASSIVE retains allocated WAL bytes. Do not checkpoint the
				// same idle WAL repeatedly after all frames were processed.
				// Partial/busy/error outcomes retry on the next bounded tick.
				if info.Size() == completedSize && info.ModTime().Equal(completedModified) {
					continue
				}
				ctx, cancel := context.WithTimeout(c.Context, 2*time.Second)
				busy, log, done, err := c.passiveCheckpoint(ctx, w.db)
				cancel()
				if err == nil && busy == 0 && done >= log {
					completedSize = info.Size()
					completedModified = info.ModTime()
				}
			}
		}
	}()
	return nil
}

func (c *Core) passiveCheckpoint(ctx context.Context, db *sql.DB) (busy, log, done int, err error) {
	started := diagnosticStart()
	err = db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &log, &done)
	c.observeCheckpoint(diagnosticElapsed(started), busy, log, done, err != nil)
	return
}
