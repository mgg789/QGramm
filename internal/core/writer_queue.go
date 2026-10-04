package core

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type messageWriteResult struct {
	message  Message
	repeated bool
	err      error
}

type messageWriteJob struct {
	ctx    context.Context
	queued time.Time
	run    func(context.Context) (Message, bool, error)
	result chan messageWriteResult
}

// messageWriter bounds retained, already validated payloads before the single
// SQLite writer. It never acknowledges work before its transaction commits.
type messageWriter struct {
	core                                                   *Core
	jobs                                                   chan messageWriteJob
	stop, done                                             chan struct{}
	mu                                                     sync.Mutex
	closed                                                 bool
	admitted, rejected, completed, cancelled               atomic.Uint64
	waitNS, serviceNS, commitNS, commitCount, commitErrors atomic.Uint64
}

type WriterMetrics struct {
	Capacity     int    `json:"capacity"`
	Queued       int    `json:"queued"`
	Admitted     uint64 `json:"admitted"`
	Rejected     uint64 `json:"rejected"`
	Completed    uint64 `json:"completed"`
	Cancelled    uint64 `json:"cancelled"`
	QueueWaitNS  uint64 `json:"queue_wait_ns"`
	ServiceNS    uint64 `json:"service_ns"`
	CommitNS     uint64 `json:"commit_ns"`
	CommitCount  uint64 `json:"commit_count"`
	CommitErrors uint64 `json:"commit_errors"`
}

func newMessageWriter(c *Core) *messageWriter {
	w := &messageWriter{core: c, jobs: make(chan messageWriteJob, min(256, max(8, c.Config.Capacity.Workers*4))), stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *messageWriter) submit(ctx context.Context, run func(context.Context) (Message, bool, error)) (Message, bool, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, false, err
	}
	job := messageWriteJob{ctx: ctx, queued: time.Now(), run: run, result: make(chan messageWriteResult, 1)}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return Message{}, false, &APIError{503, "writer stopping"}
	}
	select {
	case w.jobs <- job:
		w.admitted.Add(1)
		w.mu.Unlock()
	default:
		w.rejected.Add(1)
		w.mu.Unlock()
		return Message{}, false, &APIError{503, "writer queue full"}
	}
	// Wait for the definitive transaction outcome even if the client cancels.
	// The queued/running transaction uses ctx and will roll back when cancelled;
	// racing a successful commit must not turn its receipt into a queue rejection.
	result := <-job.result
	return result.message, result.repeated, result.err
}

func (w *messageWriter) execute(job messageWriteJob) {
	w.waitNS.Add(uint64(time.Since(job.queued)))
	ctx, cancel := context.WithCancel(job.ctx)
	stopCancel := context.AfterFunc(w.core.Context, cancel)
	start := time.Now()
	var result messageWriteResult
	if err := ctx.Err(); err != nil {
		result.err = err
		w.cancelled.Add(1)
	} else {
		result.message, result.repeated, result.err = job.run(ctx)
		if ctx.Err() != nil && result.err != nil {
			w.cancelled.Add(1)
		}
	}
	stopCancel()
	cancel()
	w.serviceNS.Add(uint64(time.Since(start)))
	w.completed.Add(1)
	job.result <- result
}

func (w *messageWriter) loop() {
	defer close(w.done)
	for {
		select {
		case job := <-w.jobs:
			w.execute(job)
		case <-w.stop:
			// close() prevents new admissions; complete all callers before DB close.
			for {
				select {
				case job := <-w.jobs:
					w.execute(job)
				default:
					return
				}
			}
		}
	}
}

func (w *messageWriter) recordCommit(elapsed time.Duration, success bool) {
	w.commitNS.Add(uint64(elapsed))
	w.commitCount.Add(1)
	if !success {
		w.commitErrors.Add(1)
	}
}

func (w *messageWriter) close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.stop)
	}
	w.mu.Unlock()
	<-w.done
}

func (c *Core) WriterStats() WriterMetrics {
	if c.writer == nil {
		return WriterMetrics{}
	}
	w := c.writer
	return WriterMetrics{Capacity: cap(w.jobs), Queued: len(w.jobs), Admitted: w.admitted.Load(), Rejected: w.rejected.Load(), Completed: w.completed.Load(), Cancelled: w.cancelled.Load(), QueueWaitNS: w.waitNS.Load(), ServiceNS: w.serviceNS.Load(), CommitNS: w.commitNS.Load(), CommitCount: w.commitCount.Load(), CommitErrors: w.commitErrors.Load()}
}
