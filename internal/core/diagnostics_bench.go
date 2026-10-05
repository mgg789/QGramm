//go:build qg_bench_profile

package core

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type timingBuckets [10]atomic.Uint64
type diagnosticState struct {
	reads                           atomic.Uint64
	wait, service, commit           timingBuckets
	body                            timingBuckets
	sqlTiming                       timingBuckets
	projection, wsWrite, checkpoint timingBuckets
	mu                              sync.Mutex
	slow                            [64]diagnosticTiming
	next                            uint64
}

type diagnosticTiming struct {
	Sequence           uint64 `json:"sequence"`
	EndUnixNano        int64  `json:"end_unix_nano"`
	DurationNS         int64  `json:"duration_ns"`
	Stage              string `json:"stage"`
	Busy               int    `json:"busy,omitempty"`
	LogFrames          int    `json:"log_frames,omitempty"`
	CheckpointedFrames int    `json:"checkpointed_frames,omitempty"`
	Failed             bool   `json:"failed,omitempty"`
}

func (c *Core) observeTiming(stage string, d time.Duration, record diagnosticTiming) {
	if d < 8*time.Millisecond && stage != "checkpoint" {
		return
	}
	record.EndUnixNano = time.Now().UnixNano()
	record.DurationNS = int64(d)
	record.Stage = stage
	c.diagnostics.mu.Lock()
	c.diagnostics.next++
	record.Sequence = c.diagnostics.next
	c.diagnostics.slow[(record.Sequence-1)%64] = record
	c.diagnostics.mu.Unlock()
}

func (b *timingBuckets) observe(d time.Duration) {
	i := 0
	for bound := 125 * time.Microsecond; i < 9 && d > bound; bound *= 2 {
		i++
	}
	b[i].Add(1)
}
func (b *timingBuckets) values() [10]uint64 {
	var out [10]uint64
	for i := range out {
		out[i] = b[i].Load()
	}
	return out
}
func (c *Core) observeRead()                          { c.diagnostics.reads.Add(1) }
func diagnosticStart() time.Time                      { return time.Now() }
func diagnosticElapsed(start time.Time) time.Duration { return time.Since(start) }
func (c *Core) observeSQL(d time.Duration) {
	c.diagnostics.sqlTiming.observe(d)
	c.observeTiming("query_lifetime", d, diagnosticTiming{})
}
func (c *Core) observeBody(d time.Duration) {
	c.diagnostics.body.observe(d)
	c.observeTiming("transaction_body", d, diagnosticTiming{})
}
func (c *Core) observeQueueWait(d time.Duration) {
	c.diagnostics.wait.observe(d)
	c.observeTiming("queue_wait", d, diagnosticTiming{})
}
func (c *Core) observeService(d time.Duration) {
	c.diagnostics.service.observe(d)
	c.observeTiming("writer_service", d, diagnosticTiming{})
}
func (c *Core) observeCommit(d time.Duration) {
	c.diagnostics.commit.observe(d)
	c.observeTiming("commit", d, diagnosticTiming{})
}
func (c *Core) observeProjection(d time.Duration) {
	c.diagnostics.projection.observe(d)
	c.observeTiming("projection", d, diagnosticTiming{})
}
func (c *Core) observeWSWrite(d time.Duration) {
	c.diagnostics.wsWrite.observe(d)
	c.observeTiming("ws_write", d, diagnosticTiming{})
}
func (c *Core) observeCheckpoint(d time.Duration, busy, log, done int, failed bool) {
	c.diagnostics.checkpoint.observe(d)
	c.observeTiming("checkpoint", d, diagnosticTiming{Busy: busy, LogFrames: log, CheckpointedFrames: done, Failed: failed})
}
func (c *Core) DiagnosticStats() map[string]any {
	c.diagnostics.mu.Lock()
	start := uint64(0)
	if c.diagnostics.next > 64 {
		start = c.diagnostics.next - 64
	}
	records := make([]diagnosticTiming, 0, c.diagnostics.next-start)
	for seq := start; seq < c.diagnostics.next; seq++ {
		records = append(records, c.diagnostics.slow[seq%64])
	}
	lastSequence := c.diagnostics.next
	c.diagnostics.mu.Unlock()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	pauses := make([]map[string]uint64, 0, 16)
	for i := uint32(0); i < min(mem.NumGC, 16); i++ {
		index := (mem.NumGC - 1 - i) % 256
		pauses = append(pauses, map[string]uint64{"end_unix_nano": mem.PauseEnd[index], "duration_ns": mem.PauseNs[index]})
	}
	var walBytes int64
	if info, err := os.Stat(c.Config.Storage.Path + "-wal"); err == nil {
		walBytes = info.Size()
	}
	return map[string]any{"sample_unix_nano": time.Now().UnixNano(), "wal_bytes": walBytes, "heap_alloc_bytes": mem.HeapAlloc, "total_alloc_bytes": mem.TotalAlloc, "mallocs": mem.Mallocs, "gc_cycles": mem.NumGC, "gc_pause_total_ns": mem.PauseTotalNs, "gc_recent_pauses": pauses, "slow_threshold_ms": 8, "slow_records_capacity": 64, "slow_last_sequence": lastSequence, "slow_records": records, "sql_read_calls": c.diagnostics.reads.Load(), "bucket_upper_ms": [10]string{"0.125", "0.25", "0.5", "1", "2", "4", "8", "16", "32", "+Inf"}, "queue_wait_jobs": c.diagnostics.wait.values(), "writer_service_transactions": c.diagnostics.service.values(), "transaction_body_calls": c.diagnostics.body.values(), "commit_transactions": c.diagnostics.commit.values(), "query_lifetime_calls": c.diagnostics.sqlTiming.values(), "projection_calls": c.diagnostics.projection.values(), "ws_write_calls": c.diagnostics.wsWrite.values(), "checkpoint_calls": c.diagnostics.checkpoint.values()}
}
