package core

import (
	"context"
	"sync/atomic"
	"time"
)

type admissionCounters struct{ admitted, rejected, waitNS atomic.Uint64 }
type AdmissionMetrics struct {
	Active   int    `json:"active"`
	Pending  int    `json:"pending"`
	Admitted uint64 `json:"admitted"`
	Rejected uint64 `json:"rejected"`
	WaitNS   uint64 `json:"wait_ns"`
}

func (c *Core) AdmissionStats() AdmissionMetrics {
	return AdmissionMetrics{len(c.httpSlots), len(c.httpPending), c.admission.admitted.Load(), c.admission.rejected.Load(), c.admission.waitNS.Load()}
}

// Bound both active requests and pending waiters. Short bursts may wait up to
// 100 ms; sustained overload gets Retry-After without retaining unlimited work.
func (c *Core) admitHTTP(ctx context.Context) bool {
	select {
	case c.httpSlots <- struct{}{}:
		c.admission.admitted.Add(1)
		return true
	default:
	}
	select {
	case c.httpPending <- struct{}{}:
	default:
		c.admission.rejected.Add(1)
		return false
	}
	defer func() { <-c.httpPending }()
	start := time.Now()
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	defer func() { c.admission.waitNS.Add(uint64(time.Since(start))) }()
	select {
	case c.httpSlots <- struct{}{}:
		c.admission.admitted.Add(1)
		return true
	case <-ctx.Done():
	case <-c.Context.Done():
	case <-timer.C:
	}
	c.admission.rejected.Add(1)
	return false
}
