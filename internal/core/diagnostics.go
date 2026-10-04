//go:build !qg_bench_profile

package core

import "time"

// Production inlines these empty hooks. Counters and histograms exist only in
// the private profile build, which has no diagnostic network endpoint.
type diagnosticState struct{}

func (c *Core) observeRead()                                         {}
func (c *Core) observeQueueWait(time.Duration)                       {}
func (c *Core) observeService(time.Duration)                         {}
func (c *Core) observeCommit(time.Duration)                          {}
func (c *Core) observeProjection(time.Duration)                      {}
func (c *Core) observeWSWrite(time.Duration)                         {}
func (c *Core) observeCheckpoint(time.Duration, int, int, int, bool) {}
func (c *Core) observeBody(time.Duration)                            {}
func (c *Core) observeSQL(time.Duration)                             {}
func diagnosticStart() time.Time                                     { return time.Time{} }
func diagnosticElapsed(time.Time) time.Duration                      { return 0 }
