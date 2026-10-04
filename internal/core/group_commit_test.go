package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

func queueSend(t *testing.T, f *fixture, ctx context.Context, in MessageInput, expected int) <-chan messageWriteResult {
	t.Helper()
	done := make(chan messageWriteResult, 1)
	go func() {
		m, err := f.c.sendMessage(ctx, f.id, "chat", in, true)
		done <- messageWriteResult{message: m, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for f.c.WriterStats().Queued < expected {
		if time.Now().After(deadline) {
			t.Fatal("message was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	return done
}

func TestGroupCommitIsolatesHookFailureCancellationAndDedup(t *testing.T) {
	f := newFixture(t)
	release := blockMessageWriter(t, f.c)
	failure := errors.New("isolated hook failure")
	f.c.InTransaction = append(f.c.InTransaction, func(ctx context.Context, tx *sql.Tx, _ Identity, _ string, m Message) error {
		if m.OperationID == "bad" {
			_, err := tx.ExecContext(ctx, `INSERT INTO users(id) VALUES('must-rollback')`)
			if err != nil {
				return err
			}
			return failure
		}
		return nil
	})
	firstInput := f.input(t, "first-group", "one")
	first := queueSend(t, f, context.Background(), firstInput, 1)
	bad := queueSend(t, f, context.Background(), f.input(t, "bad", "bad"), 2)
	retry := queueSend(t, f, context.Background(), firstInput, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := queueSend(t, f, ctx, f.input(t, "cancel-group", "cancel"), 4)
	last := queueSend(t, f, context.Background(), f.input(t, "last-group", "two"), 5)
	cancel()
	release()
	a, b, r, cancelledResult, z := <-first, <-bad, <-retry, <-cancelled, <-last
	if a.err != nil || r.err != nil || z.err != nil || !errors.Is(b.err, failure) || !errors.Is(cancelledResult.err, context.Canceled) {
		t.Fatalf("group results: %v %v %v %v %v", a.err, b.err, r.err, cancelledResult.err, z.err)
	}
	if a.message.ID != r.message.ID || a.message.Seq != 1 || z.message.Seq != 2 {
		t.Fatal("dedup or order changed")
	}
	var messages, events, operations, rolledBack int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations),(SELECT COUNT(*) FROM users WHERE id='must-rollback')`).Scan(&messages, &events, &operations, &rolledBack); err != nil {
		t.Fatal(err)
	}
	if messages != 2 || events != 2 || operations != 2 || rolledBack != 0 {
		t.Fatalf("partial state: %d/%d/%d/%d", messages, events, operations, rolledBack)
	}
	metrics := f.c.WriterStats()
	if metrics.CommitCount != 1 || metrics.CommittedMessages != 2 || metrics.MaxBatch != 5 {
		t.Fatalf("not grouped: %+v", metrics)
	}
}

// A real SQLite transaction with a controlled commit boundary. Failures roll
// the real transaction back; no fake database or in-memory persistence is used.
type commitGateDriver struct{ gate *commitGate }
type commitGate struct {
	entered, release chan struct{}
	fail             bool
}
type commitGateConn struct {
	driver.Conn
	gate *commitGate
}
type commitGateTx struct {
	driver.Tx
	gate *commitGate
}

func (d commitGateDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return &commitGateConn{c, d.gate}, nil
}
func (c *commitGateConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if cbt, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = cbt.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &commitGateTx{tx, c.gate}, nil
}
func (tx *commitGateTx) Commit() error {
	close(tx.gate.entered)
	<-tx.gate.release
	if tx.gate.fail {
		_ = tx.Tx.Rollback()
		return errors.New("injected commit failure")
	}
	return tx.Tx.Commit()
}

var commitDriverNumber atomic.Uint64

func installCommitGate(t *testing.T, f *fixture, gate *commitGate) {
	t.Helper()
	f.c.writeStatements.close()
	f.c.writeStatements = statementCache{}
	if err := f.c.DB.Close(); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("qgramm-commit-gate-%d", commitDriverNumber.Add(1))
	sql.Register(name, commitGateDriver{gate})
	dsn, err := sqliteDSN(f.cfg.Storage.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	f.c.DB, err = sql.Open(name, dsn)
	if err != nil {
		t.Fatal(err)
	}
	f.c.DB.SetMaxOpenConns(1)
	if err = f.c.prepareHotWrites(); err != nil {
		t.Fatal(err)
	}
}

func TestGroupCommitDoesNotAckOrWakeBeforeDurableCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			f := newFixture(t)
			gate := &commitGate{make(chan struct{}), make(chan struct{}), fail}
			installCommitGate(t, f, gate)
			var released atomic.Bool
			releaseGate := func() {
				if released.CompareAndSwap(false, true) {
					close(gate.release)
				}
			}
			t.Cleanup(releaseGate)
			release := blockMessageWriter(t, f.c)
			conn := &connection{wake: make(chan struct{}, 1)}
			f.c.subscribe(conn, "chat", true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			first := queueSend(t, f, ctx, f.input(t, "commit-gated-one", "one"), 1)
			second := queueSend(t, f, context.Background(), f.input(t, "commit-gated-two", "two"), 2)
			release()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("no commit")
			}
			select {
			case <-first:
				t.Fatal("early ACK")
			default:
			}
			select {
			case <-second:
				t.Fatal("early ACK")
			default:
			}
			select {
			case <-conn.wake:
				t.Fatal("early fanout")
			default:
			}
			var count int
			if err := f.c.reader().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("visible before commit: %d %v", count, err)
			}
			// Cancellation during commit cannot turn another caller's commit into
			// rollback or turn a durable accepted result into an ambiguous rejection.
			cancel()
			releaseGate()
			a, b := <-first, <-second
			if err := f.c.reader().QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if fail {
				if a.err == nil || b.err == nil || count != 0 {
					t.Fatalf("failed commit: %v %v count=%d", a.err, b.err, count)
				}
				select {
				case <-conn.wake:
					t.Fatal("failed commit woke subscribers")
				default:
				}
			} else {
				if a.err != nil || b.err != nil || count != 2 {
					t.Fatalf("successful commit: %v %v count=%d", a.err, b.err, count)
				}
			}
			f.c.mu.Lock()
			delete(f.c.subscribers, "chat")
			f.c.mu.Unlock()
		})
	}
}

func TestGroupCommitBoundedByJobsAndBytes(t *testing.T) {
	for _, size := range []int{1, maxWriteBatchBytes/2 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f := newFixture(t)
			release := blockMessageWriter(t, f.c)
			const count = 20
			done := make(chan error, count)
			for i := 0; i < count; i++ {
				go func() {
					_, _, err := f.c.writer.submitTx(context.Background(), size, func(context.Context, *sql.Tx) (Message, bool, error) { return Message{}, true, nil })
					done <- err
				}()
				deadline := time.Now().Add(5 * time.Second)
				for f.c.WriterStats().Queued < i+1 {
					if time.Now().After(deadline) {
						t.Fatal("not queued")
					}
					time.Sleep(time.Millisecond)
				}
			}
			release()
			for range count {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			m := f.c.WriterStats()
			want := uint64(maxWriteBatch)
			if size > maxWriteBatchBytes/2 {
				want = 1
			}
			if m.MaxBatch != want || m.BatchJobs != count {
				t.Fatalf("bound %+v", m)
			}
		})
	}
}

func TestGroupCommitCancelsRunningJobWithoutRollingBackNeighbour(t *testing.T) {
	f := newFixture(t)
	release := blockMessageWriter(t, f.c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.c.InTransaction = append(f.c.InTransaction, func(_ context.Context, _ *sql.Tx, _ Identity, _ string, m Message) error {
		if m.OperationID == "cancel-running" {
			cancel()
		}
		return nil
	})
	one := queueSend(t, f, ctx, f.input(t, "cancel-running", "cancel"), 1)
	two := queueSend(t, f, context.Background(), f.input(t, "healthy-neighbour", "healthy"), 2)
	release()
	a, b := <-one, <-two
	if !errors.Is(a.err, context.Canceled) || b.err != nil || b.message.Seq != 1 {
		t.Fatalf("cancel isolation: %v %+v %v", a.err, b.message, b.err)
	}
	var count int
	if err := f.c.DB.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d err %v", count, err)
	}
}

func TestGroupCommitDiskFullRollsBackTentativeNeighbours(t *testing.T) {
	f := newFixture(t)
	var pages int
	if err := f.c.DB.QueryRow(`PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := f.c.DB.Exec(`PRAGMA max_page_count=` + jsonNumber(pages)); err != nil {
		t.Fatal(err)
	}
	release := blockMessageWriter(t, f.c)
	firstInput, largeInput := f.input(t, "full-first", "one"), f.input(t, "full-large", strings.Repeat("x", 32000))
	first := queueSend(t, f, context.Background(), firstInput, 1)
	large := queueSend(t, f, context.Background(), largeInput, 2)
	release()
	a, b := <-first, <-large
	if a.err == nil || b.err == nil {
		t.Fatalf("full group acknowledged: %v %v", a.err, b.err)
	}
	var messages, events, operations, seq int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations),(SELECT seq FROM chats WHERE id='chat')`).Scan(&messages, &events, &operations, &seq); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || events != 0 || operations != 0 || seq != 0 {
		t.Fatal("partial group persisted")
	}
	if _, err := f.c.DB.Exec(`PRAGMA max_page_count=1000000`); err != nil {
		t.Fatal(err)
	}
	for _, in := range []MessageInput{firstInput, largeInput} {
		if _, err := f.c.sendMessage(context.Background(), f.id, "chat", in, true); err != nil {
			t.Fatal("retry after disk recovery", err)
		}
	}
}

func TestQueuedAttachmentMetadataOwnsItsSnapshot(t *testing.T) {
	f := newFixture(t)
	// This unit test exercises core metadata ownership; actual file admission
	// and quotas are separately covered by the files module integration tests.
	f.c.Config.Features.Files = true
	release := blockMessageWriter(t, f.c)
	in := f.input(t, "attachment-snapshot", "one")
	attachments := []string{"original-attachment"}
	in.Attachments = attachments
	done := queueSend(t, f, context.Background(), in, 1)
	attachments[0] = "caller-reused-attachment"
	release()
	result := <-done
	if result.err != nil || len(result.message.Attachments) != 1 || result.message.Attachments[0] != "original-attachment" {
		t.Fatalf("input alias leaked: %+v %v", result.message, result.err)
	}
	var metadata, saved []byte
	if err := f.c.DB.QueryRow(`SELECT msg.metadata,op.result FROM messages msg JOIN operations op ON op.device_id=msg.device_id AND op.operation_id=msg.operation_id WHERE msg.id=?`, result.message.ID).Scan(&metadata, &saved); err != nil {
		t.Fatal(err)
	}
	var meta messageMetadata
	var message Message
	if json.Unmarshal(metadata, &meta) != nil || json.Unmarshal(saved, &message) != nil || meta.Attachments[0] != message.Attachments[0] || message.Attachments[0] != "original-attachment" {
		t.Fatal("metadata and durable operation differ")
	}
}
