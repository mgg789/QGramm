//go:build qg_bench_profile

package core

import (
	"sync/atomic"
	"time"
)

type timingBuckets [10]atomic.Uint64
type diagnosticState struct {
	reads                 atomic.Uint64
	wait, service, commit timingBuckets
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
func (c *Core) observeRead()                     { c.diagnostics.reads.Add(1) }
func (c *Core) observeQueueWait(d time.Duration) { c.diagnostics.wait.observe(d) }
func (c *Core) observeService(d time.Duration)   { c.diagnostics.service.observe(d) }
func (c *Core) observeCommit(d time.Duration)    { c.diagnostics.commit.observe(d) }
func (c *Core) DiagnosticStats() map[string]any {
	return map[string]any{"sql_read_calls": c.diagnostics.reads.Load(), "bucket_upper_ms": [10]string{"0.125", "0.25", "0.5", "1", "2", "4", "8", "16", "32", "+Inf"}, "queue_wait_jobs": c.diagnostics.wait.values(), "writer_service_transactions": c.diagnostics.service.values(), "commit_transactions": c.diagnostics.commit.values()}
}
