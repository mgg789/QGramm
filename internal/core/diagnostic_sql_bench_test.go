//go:build qg_bench_profile

package core

import (
	"context"
	"testing"
)

func TestQueryLifetimeCountsResultConsumptionOnce(t *testing.T) {
	f := newFixture(t)
	count := func() uint64 {
		var sum uint64
		for _, v := range f.c.DiagnosticStats()["query_lifetime_calls"].([10]uint64) {
			sum += v
		}
		return sum
	}
	start := count()
	row := f.c.readQueryRow(context.Background(), "SELECT 1")
	if count() != start {
		t.Fatal("row timing ended before result consumption")
	}
	var value int
	if err := row.Scan(&value); err != nil || value != 1 {
		t.Fatal("row result changed", err)
	}
	_ = row.Scan(&value)
	if count() != start+1 {
		t.Fatal("row lifetime counted more than once")
	}
	rows, err := f.c.readQuery(context.Background(), "SELECT 1 UNION ALL SELECT 2")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if count() != start+2 {
		t.Fatal("rows lifetime not counted exactly once")
	}
	if _, err := f.c.readQuery(context.Background(), "SELECT missing_column FROM users"); err == nil || count() != start+3 {
		t.Fatal("failed query lifetime not counted")
	}
}
