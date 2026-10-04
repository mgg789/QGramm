package core

import (
	"context"
	"database/sql"
	"errors"
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
	runTx  func(context.Context, *sql.Tx) (Message, bool, error)
	bytes  int
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
	batchCount, batchJobs, committedMessages, maxBatch     atomic.Uint64
}

type WriterMetrics struct {
	Capacity          int    `json:"capacity"`
	Queued            int    `json:"queued"`
	Admitted          uint64 `json:"admitted"`
	Rejected          uint64 `json:"rejected"`
	Completed         uint64 `json:"completed"`
	Cancelled         uint64 `json:"cancelled"`
	QueueWaitNS       uint64 `json:"queue_wait_ns"`
	ServiceNS         uint64 `json:"service_ns"`
	CommitNS          uint64 `json:"commit_ns"`
	CommitCount       uint64 `json:"commit_count"`
	CommitErrors      uint64 `json:"commit_errors"`
	BatchCount        uint64 `json:"batch_count"`
	BatchJobs         uint64 `json:"batch_jobs"`
	CommittedMessages uint64 `json:"committed_messages"`
	MaxBatch          uint64 `json:"max_batch"`
}

func newMessageWriter(c *Core) *messageWriter {
	w := &messageWriter{core: c, jobs: make(chan messageWriteJob, min(256, max(8, c.Config.Capacity.Workers*4))), stop: make(chan struct{}), done: make(chan struct{})}
	go w.loop()
	return w
}

func (w *messageWriter) submit(ctx context.Context, run func(context.Context) (Message, bool, error)) (Message, bool, error) {
	return w.enqueue(messageWriteJob{ctx: ctx, run: run})
}

func (w *messageWriter) submitTx(ctx context.Context, bytes int, run func(context.Context, *sql.Tx) (Message, bool, error)) (Message, bool, error) {
	return w.enqueue(messageWriteJob{ctx: ctx, bytes: bytes, runTx: run})
}

func (w *messageWriter) enqueue(job messageWriteJob) (Message, bool, error) {
	ctx := job.ctx
	if err := ctx.Err(); err != nil {
		return Message{}, false, err
	}
	job.queued, job.result = time.Now(), make(chan messageWriteResult, 1)
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
	wait := time.Since(job.queued)
	w.waitNS.Add(uint64(wait))
	w.core.observeQueueWait(wait)
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
	service := time.Since(start)
	w.serviceNS.Add(uint64(service))
	w.core.observeService(service)
	w.completed.Add(1)
	job.result <- result
}

func (w *messageWriter) loop() {
	defer close(w.done)
	var pending *messageWriteJob
	for {
		if pending != nil {
			pending = w.process(*pending)
			continue
		}
		select {
		case job := <-w.jobs:
			pending = w.process(job)
		case <-w.stop:
			// close() prevents new admissions; complete all callers before DB close.
			for {
				select {
				case job := <-w.jobs:
					pending = w.process(job)
					for pending != nil {
						pending = w.process(*pending)
					}
				default:
					return
				}
			}
		}
	}
}

const maxWriteBatch = 16
const maxWriteBatchBytes = 8 << 20

// Only already queued message jobs share a commit. There is no batching timer.
// Oversized single jobs run alone; the payload limit remains the configured one.
func (w *messageWriter) process(first messageWriteJob) *messageWriteJob {
	if first.runTx == nil {
		w.execute(first)
		return nil
	}
	var jobs [maxWriteBatch]messageWriteJob
	jobs[0] = first
	n, bytes := 1, first.bytes
	var pending *messageWriteJob
drain:
	for n < len(jobs) && bytes < maxWriteBatchBytes {
		select {
		case job := <-w.jobs:
			if job.runTx == nil || job.bytes > maxWriteBatchBytes-bytes {
				pending = &job
				break drain
			}
			jobs[n], n, bytes = job, n+1, bytes+job.bytes
		default:
			break drain
		}
	}
	w.executeBatch(jobs[:n])
	return pending
}

// Each job is isolated by a savepoint. A failed/cancelled job cannot leave a
// sequence, event, operation or module state behind in a successful neighbour.
// Successful results remain private until the shared FULL commit completes.
func (w *messageWriter) executeBatch(jobs []messageWriteJob) {
	start := time.Now()
	var results [maxWriteBatch]messageWriteResult
	tx, batchErr := w.core.DB.BeginTx(w.core.Context, nil)
	if tx != nil {
		defer tx.Rollback()
	}
	changed := 0
	for i, job := range jobs {
		wait := time.Since(job.queued)
		w.waitNS.Add(uint64(wait))
		w.core.observeQueueWait(wait)
		result := &results[i]
		if err := job.ctx.Err(); err != nil {
			result.err = err
			w.cancelled.Add(1)
			continue
		}
		if batchErr != nil {
			result.err = batchErr
			continue
		}
		// The single-job path needs no savepoint or extra SQL round trips.
		if len(jobs) > 1 {
			_, batchErr = tx.ExecContext(w.core.Context, "SAVEPOINT qgramm_message")
			if batchErr != nil {
				result.err = batchErr
				continue
			}
		}
		result.message, result.repeated, result.err = job.runTx(job.ctx, tx)
		if result.err == nil {
			result.err = job.ctx.Err()
		}
		if result.err != nil {
			if job.ctx.Err() != nil {
				w.cancelled.Add(1)
			}
			if len(jobs) == 1 {
				batchErr = result.err
				continue
			}
			_, batchErr = tx.ExecContext(w.core.Context, "ROLLBACK TO qgramm_message")
		}
		if len(jobs) > 1 && batchErr == nil {
			_, batchErr = tx.ExecContext(w.core.Context, "RELEASE qgramm_message")
		}
		if result.err == nil && batchErr == nil {
			if !result.repeated {
				changed++
			}
		}
	}
	if batchErr == nil && changed > 0 {
		commitStart := time.Now()
		batchErr = tx.Commit()
		w.recordCommit(time.Since(commitStart), batchErr == nil)
		if batchErr == nil {
			w.committedMessages.Add(uint64(changed))
		}
	} else if tx != nil {
		if err := tx.Rollback(); batchErr == nil && err != nil && !errors.Is(err, sql.ErrTxDone) {
			batchErr = err
		}
	}
	// A failed outer transaction invalidates every tentative successful job,
	// including an in-batch retry whose original message was not yet durable.
	if batchErr != nil {
		for i := range jobs {
			if results[i].err == nil {
				results[i] = messageWriteResult{err: batchErr}
			}
		}
	}
	w.batchCount.Add(1)
	w.batchJobs.Add(uint64(len(jobs)))
	for old := w.maxBatch.Load(); uint64(len(jobs)) > old && !w.maxBatch.CompareAndSwap(old, uint64(len(jobs))); old = w.maxBatch.Load() {
	}
	service := time.Since(start)
	w.serviceNS.Add(uint64(service))
	w.core.observeService(service)
	w.completed.Add(uint64(len(jobs)))
	for i, job := range jobs {
		job.result <- results[i]
	}
}

func (w *messageWriter) recordCommit(elapsed time.Duration, success bool) {
	w.core.observeCommit(elapsed)
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
	return WriterMetrics{Capacity: cap(w.jobs), Queued: len(w.jobs), Admitted: w.admitted.Load(), Rejected: w.rejected.Load(), Completed: w.completed.Load(), Cancelled: w.cancelled.Load(), QueueWaitNS: w.waitNS.Load(), ServiceNS: w.serviceNS.Load(), CommitNS: w.commitNS.Load(), CommitCount: w.commitCount.Load(), CommitErrors: w.commitErrors.Load(), BatchCount: w.batchCount.Load(), BatchJobs: w.batchJobs.Load(), CommittedMessages: w.committedMessages.Load(), MaxBatch: w.maxBatch.Load()}
}
