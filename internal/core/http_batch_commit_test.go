package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func batchRequest(t *testing.T, f *fixture, ctx context.Context, inputs []MessageInput, minimal bool) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"messages": inputs})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	r.SetPathValue("chat", "chat")
	if minimal {
		r.Header.Set("Prefer", "return=minimal")
	}
	w := httptest.NewRecorder()
	f.c.batch(w, r, f.id)
	return w
}

func batchStatuses(t *testing.T, w *httptest.ResponseRecorder) []int {
	t.Helper()
	var out []struct {
		Status int `json:"status"`
	}
	if w.Code != 207 {
		t.Fatalf("batch status %d: %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	statuses := make([]int, len(out))
	for i := range out {
		statuses[i] = out[i].Status
	}
	return statuses
}

func TestHTTPBatchSharesCommitAndIsolatesFailure(t *testing.T) {
	for _, minimal := range []bool{false, true} {
		t.Run(fmt.Sprint(minimal), func(t *testing.T) {
			f := newFixture(t)
			f.c.InTransaction = append(f.c.InTransaction, func(ctx context.Context, tx *sql.Tx, _ Identity, _ string, m Message) error {
				if m.OperationID != "bad-batch" {
					return nil
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO users(id) VALUES('batch-rollback')"); err != nil {
					return err
				}
				return errors.New("batch hook failed")
			})
			in := []MessageInput{f.input(t, "batch-a", "a"), f.input(t, "bad-batch", "bad"), f.input(t, "batch-b", "b")}
			w := batchRequest(t, f, context.Background(), in, minimal)
			codes := batchStatuses(t, w)
			if fmt.Sprint(codes) != "[201 503 201]" {
				t.Fatalf("statuses %v", codes)
			}
			stats := f.c.WriterStats()
			if stats.CommitCount != 1 || stats.CommittedMessages != 2 || stats.MaxBatch != 3 {
				t.Fatalf("not one FULL commit: %+v", stats)
			}
			var count int
			if err := f.c.DB.QueryRow("SELECT COUNT(*) FROM users WHERE id='batch-rollback'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("savepoint leaked %d %v", count, err)
			}
			retry := batchRequest(t, f, context.Background(), in, minimal)
			if fmt.Sprint(batchStatuses(t, retry)) != "[201 503 201]" {
				t.Fatal(retry.Body)
			}
			if f.c.WriterStats().CommitCount != 1 {
				t.Fatal("retry added commit")
			}
			events, err := f.c.Events(context.Background(), Identity{"bob", "bob-phone"}, "chat", 0, 100)
			if err != nil || len(events) != 2 {
				t.Fatalf("events %d %v", len(events), err)
			}
		})
	}
}

func TestHTTPBatchRepeatedOperationAndMalformedConflict(t *testing.T) {
	f := newFixture(t)
	first := f.input(t, "same-op", "a")
	codes := batchStatuses(t, batchRequest(t, f, context.Background(), []MessageInput{first, first, {OperationID: "same-op", MLS: "malformed"}, f.input(t, "new-op", "b")}, true))
	if fmt.Sprint(codes) != "[201 201 409 201]" {
		t.Fatalf("sequential retry semantics %v", codes)
	}
	var n int
	if err := f.c.DB.QueryRow("SELECT COUNT(*) FROM messages").Scan(&n); err != nil || n != 2 {
		t.Fatalf("count %d %v", n, err)
	}
}

func TestHTTPBatchNoResponseOrWakeBeforeCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			f := newFixture(t)
			gate := &commitGate{make(chan struct{}), make(chan struct{}), fail}
			installCommitGate(t, f, gate)
			t.Cleanup(func() {
				select {
				case <-gate.release:
				default:
					close(gate.release)
				}
			})
			conn := &connection{wake: make(chan struct{}, 1)}
			f.c.subscribe(conn, "chat", true)
			in := []MessageInput{f.input(t, "gate-batch-a", "a"), f.input(t, "gate-batch-b", "b")}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- batchRequest(t, f, context.Background(), in, true) }()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("commit not entered")
			}
			select {
			case <-done:
				t.Fatal("early ACK")
			default:
			}
			select {
			case <-conn.wake:
				t.Fatal("early Wake")
			default:
			}
			var n int
			if err := f.c.reader().QueryRow("SELECT COUNT(*) FROM messages").Scan(&n); err != nil || n != 0 {
				t.Fatalf("precommit visible %d %v", n, err)
			}
			close(gate.release)
			codes := batchStatuses(t, <-done)
			if fail {
				if fmt.Sprint(codes) != "[503 503]" {
					t.Fatalf("failed commit %v", codes)
				}
				select {
				case <-conn.wake:
					t.Fatal("failed Wake")
				default:
				}
			} else if fmt.Sprint(codes) != "[201 201]" {
				t.Fatalf("accepted %v", codes)
			}
			f.c.mu.Lock()
			delete(f.c.subscribers, "chat")
			f.c.mu.Unlock()
		})
	}
}

func TestHTTPBatchSplitsAtQueueCapacity(t *testing.T) {
	f := newFixture(t)
	f.c.writer.close()
	f.c.Config.Capacity.Workers = 1
	f.c.writer = newMessageWriter(f.c)
	n := min(f.c.Config.Policy.MaxBatch, cap(f.c.writer.jobs)+1)
	in := make([]MessageInput, n)
	for i := range in {
		in[i] = f.input(t, fmt.Sprintf("split-%d", i), "payload")
	}
	for _, code := range batchStatuses(t, batchRequest(t, f, context.Background(), in, true)) {
		if code != 201 {
			t.Fatal(code)
		}
	}
	stats := f.c.WriterStats()
	want := (n + min(maxWriteBatch, cap(f.c.writer.jobs)) - 1) / min(maxWriteBatch, cap(f.c.writer.jobs))
	if stats.CommitCount != uint64(want) {
		t.Fatalf("splitting %+v expected %d commits", stats, want)
	}
}

func TestWriterGroupAdmissionCancellationAndShutdown(t *testing.T) {
	f := newFixture(t)
	f.c.writer.close()
	f.c.Config.Capacity.Workers = 1
	f.c.writer = newMessageWriter(f.c)
	release := blockMessageWriter(t, f.c)
	ctx, cancel := context.WithCancel(context.Background())
	jobs := []messageWriteJob{
		{ctx: ctx, runTx: func(context.Context, *sql.Tx) (Message, bool, error) {
			t.Error("cancelled group item ran")
			return Message{}, true, nil
		}},
		{ctx: context.Background(), runTx: func(context.Context, *sql.Tx) (Message, bool, error) {
			return Message{ID: "valid-neighbour"}, true, nil
		}},
	}
	done := make(chan []messageWriteResult, 1)
	go func() { done <- f.c.writer.submitGroup(jobs) }()
	deadline := time.Now().Add(5 * time.Second)
	for f.c.WriterStats().Queued != 2 {
		if time.Now().After(deadline) {
			t.Fatal("group not queued")
		}
		time.Sleep(time.Millisecond)
	}
	// Fill the remaining real queue capacity with another explicit group.
	fill := make([]messageWriteJob, cap(f.c.writer.jobs)-2)
	for i := range fill {
		fill[i] = jobs[1]
	}
	filled := make(chan []messageWriteResult, 1)
	go func() { filled <- f.c.writer.submitGroup(fill) }()
	deadline = time.Now().Add(5 * time.Second)
	for f.c.WriterStats().Queued != cap(f.c.writer.jobs) {
		if time.Now().After(deadline) {
			t.Fatal("queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	rejected := f.c.writer.submitGroup([]messageWriteJob{jobs[1], jobs[1]})
	if rejected[0].err == nil || rejected[1].err == nil {
		t.Fatal("group bypassed capacity")
	}
	cancel()
	closed := make(chan struct{})
	go func() { f.c.writer.close(); close(closed) }()
	release()
	results := <-done
	if !errors.Is(results[0].err, context.Canceled) || results[1].err != nil {
		t.Fatalf("isolated cancellation %v %v", results[0].err, results[1].err)
	}
	for _, result := range <-filled {
		if result.err != nil {
			t.Fatal(result.err)
		}
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain group")
	}
	if f.c.WriterStats().Queued != 0 {
		t.Fatal("group admission leaked")
	}
	if got := f.c.writer.submitGroup([]messageWriteJob{jobs[1], jobs[1]}); got[0].err == nil || got[1].err == nil {
		t.Fatal("closed writer admitted group")
	}
}

func TestDirectSendWithoutWriterUsesCallerContext(t *testing.T) {
	f := newFixture(t)
	cancelledCoreContext, cancelCore := context.WithCancel(context.Background())
	cancelCore()
	embedded := &Core{DB: f.c.DB, readDB: f.c.readDB, Config: f.c.Config, Engine: f.c.Engine, Context: cancelledCoreContext}
	t.Cleanup(func() { embedded.readStatements.close(); embedded.writeStatements.close() })
	// Embedding callers may stop background workers while retaining synchronous
	// Send; core lifecycle cancellation must not cancel their live transaction.
	msg, err := embedded.sendMessage(context.Background(), f.id, "chat", f.input(t, "direct-live", "durable"), true)
	if err != nil || msg.ID == "" {
		t.Fatalf("live caller rejected: %v", err)
	}
	var messages, events, operations int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations)`).Scan(&messages, &events, &operations); err != nil || messages != 1 || events != 1 || operations != 1 {
		t.Fatalf("not durable: %d/%d/%d %v", messages, events, operations, err)
	}
	cancelledCaller, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	if _, err := embedded.sendMessage(cancelledCaller, f.id, "chat", f.input(t, "direct-cancelled", "rejected"), true); err == nil {
		t.Fatal("cancelled caller accepted")
	}
	if err := f.c.DB.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&operations); err != nil || operations != 1 {
		t.Fatalf("cancelled caller persisted operation: %d %v", operations, err)
	}
}
