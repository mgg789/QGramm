//go:build qg_bench_profile

package core

import (
	"context"
	"fmt"
	"testing"
)

func TestBatchProjectionReadCountIsBoundedByChunks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	results := make([]messageWriteResult, maxWriteBatch+3)
	for i := range results {
		m, err := f.c.Send(ctx, f.id, "chat", f.input(t, fmt.Sprintf("projection-count-%d", i), "counted"))
		if err != nil {
			t.Fatal(err)
		}
		results[i].message = m
	}
	before := f.c.diagnostics.reads.Load()
	f.c.projectBatchResults(ctx, f.id, results)
	if reads := f.c.diagnostics.reads.Load() - before; reads != 2 {
		t.Fatalf("19 distinct projections made %d read queries, wanted 2 bounded queries", reads)
	}
	for _, result := range results {
		if result.err != nil || result.message.Envelope == nil {
			t.Fatal("bounded projection incomplete", result.err)
		}
	}
}
