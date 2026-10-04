//go:build !qg_bench_profile

package core

import "time"

// Production inlines these empty hooks. Counters and histograms exist only in
// the private profile build, which has no diagnostic network endpoint.
type diagnosticState struct{}

func (c *Core) observeRead()                   {}
func (c *Core) observeQueueWait(time.Duration) {}
func (c *Core) observeService(time.Duration)   {}
func (c *Core) observeCommit(time.Duration)    {}
