//go:build qg_bench_profile

package core

import (
	"testing"
	"time"
)

func TestPrivateTimelineIsBoundedAndContainsNoIdentity(t *testing.T) {
	c := &Core{}
	c.observeCommit(time.Millisecond)
	for i := 0; i < 70; i++ {
		c.observeCommit(9 * time.Millisecond)
	}
	stats := c.DiagnosticStats()
	records := stats["slow_records"].([]diagnosticTiming)
	if len(records) != 64 || records[0].Sequence != 7 || records[63].Sequence != 70 {
		t.Fatalf("ring window wrong: %v", records)
	}
	for _, record := range records {
		if record.Stage != "commit" || record.DurationNS != int64(9*time.Millisecond) || record.EndUnixNano == 0 {
			t.Fatalf("wrong record: %+v", record)
		}
	}
	if stats["gc_recent_pauses"] == nil || stats["sample_unix_nano"] == nil {
		t.Fatal("missing GC/time sampling")
	}
}

func TestCheckpointWorkerSkipsCompletedIdleWALAndResumesAfterWrite(t *testing.T) {
	f := newFixture(t)
	f.c.Config.Storage.CheckpointIntervalMS = 100
	f.c.Config.Storage.CheckpointWALBytes = 65536
	// Make the retained WAL exceed the trigger without reaching the default
	// auto-checkpoint threshold. PASSIVE should process it, then stay idle.
	if _, err := f.c.DB.Exec("INSERT INTO users(id) VALUES(hex(randomblob(100000)))"); err != nil {
		t.Fatal(err)
	}
	if err := f.c.startCheckpointer(); err != nil {
		t.Fatal(err)
	}
	count := func() uint64 {
		var total uint64
		for _, n := range f.c.diagnostics.checkpoint.values() {
			total += n
		}
		return total
	}
	deadline := time.Now().Add(2 * time.Second)
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	first := count()
	if first == 0 {
		t.Fatal("worker did not checkpoint WAL")
	}
	time.Sleep(250 * time.Millisecond)
	if got := count(); got != first {
		t.Fatalf("idle WAL checkpointed again: %d to%d", first, got)
	}
	if _, err := f.c.DB.Exec("UPDATE users SET disabled=1 WHERE id='alice'"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for count() == first && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if count() == first {
		t.Fatal("new WAL write did not resume checkpoints")
	}
}
