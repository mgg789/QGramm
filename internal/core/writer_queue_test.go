package core

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func blockMessageWriter(t *testing.T, c *Core) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _, _ = c.writer.submit(context.Background(), func(context.Context) (Message, bool, error) {
			close(started)
			<-release
			return Message{}, false, nil
		})
	}()
	<-started
	var once sync.Once
	unblock := func() { once.Do(func() { close(release); <-finished }) }
	t.Cleanup(unblock)
	return unblock
}

func waitQueued(t *testing.T, c *Core) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.WriterStats().Queued == 0 {
		if time.Now().After(deadline) {
			t.Fatal("send did not enter bounded queue")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestQueuedSendRechecksRevokedAccess(t *testing.T) {
	f := newFixture(t)
	release := blockMessageWriter(t, f.c)
	in := f.input(t, "queued-revoked", "secret")
	done := make(chan error, 1)
	go func() { _, err := f.c.sendMessage(context.Background(), f.id, "chat", in, true); done <- err }()
	waitQueued(t, f.c)
	if _, err := f.c.DB.Exec(`UPDATE members SET can_send=0 WHERE chat_id='chat' AND user_id='alice'`); err != nil {
		t.Fatal(err)
	}
	release()
	var api *APIError
	if err := <-done; !errors.As(err, &api) || api.Status != 403 {
		t.Fatalf("revoked queued send: %v", err)
	}
	var messages, events, operations int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations)`).Scan(&messages, &events, &operations); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || events != 0 || operations != 0 {
		t.Fatalf("revoked send persisted: %d/%d/%d", messages, events, operations)
	}
}

func TestQueuedSendCancellationDoesNotPersist(t *testing.T) {
	f := newFixture(t)
	release := blockMessageWriter(t, f.c)
	ctx, cancel := context.WithCancel(context.Background())
	in := f.input(t, "queued-cancel", "secret")
	done := make(chan error, 1)
	go func() { _, err := f.c.sendMessage(ctx, f.id, "chat", in, true); done <- err }()
	waitQueued(t, f.c)
	cancel()
	release()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled send: %v", err)
	}
	var count int
	if err := f.c.DB.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("cancelled send retained %d operations", count)
	}
}

func TestConcurrentQueuedRetriesHaveOneDurableResult(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "queued-retry", "secret")
	const callers = 8
	results := make(chan Message, callers)
	errors := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := f.c.sendMessage(context.Background(), f.id, "chat", in, true)
			results <- m
			errors <- err
		}()
	}
	wg.Wait()
	var first string
	for range callers {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		m := <-results
		if first == "" {
			first = m.ID
		}
		if m.ID != first || m.Seq != 1 {
			t.Fatalf("retry changed durable result: %+v", m)
		}
	}
	var messages, events, operations int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations)`).Scan(&messages, &events, &operations); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || events != 1 || operations != 1 {
		t.Fatalf("duplicate persisted: %d/%d/%d", messages, events, operations)
	}
	if metrics := f.c.WriterStats(); metrics.CommitCount != 1 || metrics.CommitErrors != 0 {
		t.Fatalf("unexpected commits: %+v", metrics)
	}
}

func TestQueuedTransactionFailureLeavesRetryableOperation(t *testing.T) {
	f := newFixture(t)
	in := f.input(t, "queued-hook-failure", "secret")
	failure := errors.New("injected transaction failure")
	f.c.InTransaction = append(f.c.InTransaction, func(context.Context, *sql.Tx, Identity, string, Message) error { return failure })
	if _, err := f.c.sendMessage(context.Background(), f.id, "chat", in, true); !errors.Is(err, failure) {
		t.Fatalf("injected fault: %v", err)
	}
	var messages, events, operations, seq int
	if err := f.c.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM messages),(SELECT COUNT(*) FROM events),(SELECT COUNT(*) FROM operations),(SELECT seq FROM chats WHERE id='chat')`).Scan(&messages, &events, &operations, &seq); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || events != 0 || operations != 0 || seq != 0 {
		t.Fatalf("failed transaction persisted state: %d/%d/%d seq=%d", messages, events, operations, seq)
	}
	f.c.InTransaction = f.c.InTransaction[:len(f.c.InTransaction)-1]
	if message, err := f.c.sendMessage(context.Background(), f.id, "chat", in, true); err != nil || message.Seq != 1 {
		t.Fatalf("retry failed: %+v %v", message, err)
	}
}

func TestWriterQueueFullRejectsBeforeExecutingAndCloseDrains(t *testing.T) {
	f := newFixture(t)
	release := blockMessageWriter(t, f.c)
	count := cap(f.c.writer.jobs)
	done := make(chan error, count)
	for range count {
		go func() {
			_, _, err := f.c.writer.submit(context.Background(), func(context.Context) (Message, bool, error) { return Message{}, false, nil })
			done <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.c.writer.jobs) != count {
		if time.Now().After(deadline) {
			t.Fatal("queue did not fill")
		}
		time.Sleep(time.Millisecond)
	}
	ran := false
	_, _, err := f.c.writer.submit(context.Background(), func(context.Context) (Message, bool, error) { ran = true; return Message{}, false, nil })
	var api *APIError
	if !errors.As(err, &api) || api.Status != 503 || ran {
		t.Fatalf("full queue result: %v ran=%v", err, ran)
	}
	closed := make(chan struct{})
	go func() { f.c.writer.close(); close(closed) }()
	release()
	for range count {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("writer close did not drain")
	}
	if _, _, err = f.c.writer.submit(context.Background(), func(context.Context) (Message, bool, error) { return Message{}, false, nil }); err == nil {
		t.Fatal("closed writer admitted work")
	}
}
