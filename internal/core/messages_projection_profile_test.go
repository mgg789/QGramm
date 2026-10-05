//go:build qg_bench_profile

package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectionReadCounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.c.Send(ctx, f.id, "chat", f.input(t, "read-count", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	id := Identity{"bob", "bob-phone"}
	before := f.c.diagnostics.reads.Load()
	r := httptest.NewRequest(http.MethodGet, "/?after=0&limit=100", nil)
	r.SetPathValue("chat", "chat")
	w := httptest.NewRecorder()
	f.c.history(w, r, id)
	if w.Code != 200 {
		t.Fatal("history", w.Code)
	}
	if reads := f.c.diagnostics.reads.Load() - before; reads != 1 {
		t.Fatalf("history helper reads=%d want1", reads)
	}
	before = f.c.diagnostics.reads.Load()
	if events, err := f.c.Events(ctx, id, "chat", 0, 200); err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	if reads := f.c.diagnostics.reads.Load() - before; reads != 2 {
		t.Fatalf("nonempty event helper reads=%d want2", reads)
	}
	before = f.c.diagnostics.reads.Load()
	if events, err := f.c.Events(ctx, id, "chat", m.Seq, 200); err != nil || len(events) != 0 {
		t.Fatal(events, err)
	}
	if reads := f.c.diagnostics.reads.Load() - before; reads != 1 {
		t.Fatalf("empty event helper reads=%d want1", reads)
	}
}
