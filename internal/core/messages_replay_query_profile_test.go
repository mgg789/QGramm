//go:build qg_bench_profile

package core

import (
	"context"
	"testing"
)

func TestSingleEventReplayUsesOneReadForEventAndMessage(t *testing.T) {
	f := newFixture(t)
	if _, err := f.c.Send(context.Background(), f.id, "chat", f.input(t, "single-read", "hello")); err != nil {
		t.Fatal(err)
	}
	before := f.c.diagnostics.reads.Load()
	events, err := f.c.Events(context.Background(), Identity{"bob", "bob-phone"}, "chat", 0, 200)
	if err != nil || len(events) != 1 || events[0].MessageID == "" {
		t.Fatal("single-event replay", events, err)
	}
	if reads := f.c.diagnostics.reads.Load() - before; reads != 1 {
		t.Fatalf("single-event replay reads=%d want1 after merging metadata and projection", reads)
	}
}
