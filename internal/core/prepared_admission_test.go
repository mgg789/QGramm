package core

import (
	"context"
	"testing"
	"time"
)

func TestPreparedWritesStayTransactionSafe(t *testing.T) {
	f := newFixture(t)
	tx, err := f.c.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var value int
	// A cache miss must use the active transaction, never wait for its own sole connection.
	if err := f.c.txQueryRow(ctx, tx, "SELECT 123").Scan(&value); err != nil || value != 123 {
		t.Fatalf("cold transaction query %d: %v", value, err)
	}
	if len(f.c.writeStatements.entries) == 0 {
		t.Fatal("hot writes not prepared before transactions")
	}
}
func TestPreparedCacheBound(t *testing.T) {
	f := newFixture(t)
	for i := 1; i <= 200; i++ {
		query := "SELECT ?"
		for j := 1; j < i; j++ {
			query += ",?"
		}
		_ = f.c.readStatements.get(context.Background(), f.c.reader(), query, true)
	}
	if n := len(f.c.readStatements.entries); n != 128 {
		t.Fatalf("cache entries %d", n)
	}
}
func TestAdmissionBoundsPendingAndCancellation(t *testing.T) {
	c := &Core{Context: context.Background(), httpSlots: make(chan struct{}, 1), httpPending: make(chan struct{}, 1)}
	c.httpSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 1)
	go func() { done <- c.admitHTTP(ctx) }()
	deadline := time.Now().Add(time.Second)
	for len(c.httpPending) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("waiter not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if c.admitHTTP(context.Background()) {
		t.Fatal("overflow admitted")
	}
	cancel()
	if <-done {
		t.Fatal("cancelled waiter admitted")
	}
	if len(c.httpPending) != 0 || len(c.httpSlots) != 1 {
		t.Fatal("admission leaked slot")
	}
	<-c.httpSlots
	if !c.admitHTTP(context.Background()) {
		t.Fatal("slot unavailable after cancellation")
	}
}
