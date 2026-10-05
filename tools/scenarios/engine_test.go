package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type testAdapter struct {
	send       func(context.Context, Publication) error
	history    []Publication
	disconnect func(context.Context, []int) error
	reconnect  func(context.Context, []int) error
}

func (a *testAdapter) Send(c context.Context, p Publication) error { return a.send(c, p) }
func (a *testAdapter) History(_ context.Context, p []Publication) (HistoryResult, error) {
	a.history = p
	return HistoryResult{Checked: len(p), Supported: true}, nil
}
func (a *testAdapter) Disconnect(ctx context.Context, ids []int) error {
	if a.disconnect != nil {
		return a.disconnect(ctx, ids)
	}
	return nil
}
func (a *testAdapter) Reconnect(ctx context.Context, ids []int) error {
	if a.reconnect != nil {
		return a.reconnect(ctx, ids)
	}
	return nil
}
func (a *testAdapter) Close() error  { return nil }
func (a *testAdapter) Errors() int64 { return 0 }
func testConfig() Config {
	return Config{Service: "test", Users: 2, Chats: 1, Fanout: 1, PayloadBytes: 17, Workers: 1, Drain: 5 * time.Millisecond, Steps: []Step{{"test", 100, 30 * time.Millisecond}}}
}
func TestEngineReceiveBeforeACK(t *testing.T) {
	e := newEngine(testConfig())
	a := &testAdapter{}
	a.send = func(_ context.Context, p Publication) error {
		e.receive(Delivery{p.ID, p.Chat, 1, 0, p.Payload})
		return nil
	}
	r := e.run(context.Background(), a)
	if failed(r) || r.Integrity.Accepted != 3 || r.Integrity.Received != 3 || len(a.history) != 3 {
		t.Fatalf("incorrect accounting: %+v", r)
	}
	if r.Phases[0].ACK.Samples != 3 || r.Phases[0].Delivery.Samples != 3 {
		t.Fatal("missing latency observations")
	}
}

func TestCountedAtLeastOnceDuplicateFailsStrictLoadSLO(t *testing.T) {
	e := newEngine(testConfig())
	a := &testAdapter{}
	a.send = func(_ context.Context, p Publication) error {
		d := Delivery{p.ID, p.Chat, 1, 0, p.Payload}
		e.receive(d)
		if p.ID == "00000000000000001" {
			e.receive(d)
		}
		return nil
	}
	r := e.run(context.Background(), a)
	if !r.IntegrityPassed || failed(r) || r.Integrity.Missing != 0 || r.Integrity.Received != 3 || r.Integrity.Duplicate != 1 {
		t.Fatalf("at-least-once integrity misrepresented: %+v", r)
	}
	if r.LoadPass || r.Phases[0].SLOPass || r.Phases[0].Duplicate != 1 || r.HighestTestedSLORate != 0 || r.DuplicatePolicy == "" {
		t.Fatalf("duplicate incorrectly qualified strict load SLO: %+v", r)
	}
}
func TestEngineRejectionIsNotLoss(t *testing.T) {
	e := newEngine(testConfig())
	a := &testAdapter{send: func(context.Context, Publication) error {
		return &SendError{Status: 429, Backpressure: true, Detail: "busy"}
	}}
	r := e.run(context.Background(), a)
	if failed(r) || r.Integrity.Missing != 0 || r.Phases[0].Backpressure != 3 || len(a.history) != 0 {
		t.Fatalf("rejection incorrectly treated as durable loss: %+v", r)
	}
}
func TestEngineLostAcceptedAndUncertainFail(t *testing.T) {
	for _, err := range []error{nil, errors.New("timeout")} {
		e := newEngine(testConfig())
		a := &testAdapter{send: func(context.Context, Publication) error { return err }}
		r := e.run(context.Background(), a)
		if !failed(r) {
			t.Fatal("incomplete outcome passed")
		}
		if err == nil && r.Integrity.Missing != 3 {
			t.Fatalf("missing accepted count: %+v", r)
		}
	}
}
func TestEngineConcurrencyDoesNotCatchUp(t *testing.T) {
	c := testConfig()
	c.Drain = 30 * time.Millisecond
	e := newEngine(c)
	a := &testAdapter{send: func(ctx context.Context, _ Publication) error {
		select {
		case <-time.After(24 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	r := e.run(context.Background(), a)
	p := r.Phases[0]
	if p.Offered != 1 || p.Skipped != 2 || p.Scheduled != 3 {
		t.Fatalf("scheduler violated bounded open-loop: %+v", p)
	}
}

func TestEngineReconnectExcludesRejectedOperations(t *testing.T) {
	c := testConfig()
	c.ReconnectCount = 1
	c.Offline = 250 * time.Millisecond
	c.Drain = 30 * time.Millisecond
	c.Steps = []Step{{"before", 10, 100 * time.Millisecond}, {"offline", 10, 300 * time.Millisecond}, {"after", 10, 100 * time.Millisecond}}
	e := newEngine(c)
	a := &testAdapter{}
	var mu sync.Mutex
	offline := false
	var queued []Publication
	a.disconnect = func(_ context.Context, ids []int) error {
		if len(ids) != 1 || ids[0] != 1 {
			t.Errorf("wrong receivers: %v", ids)
		}
		mu.Lock()
		offline = true
		mu.Unlock()
		return nil
	}
	a.send = func(_ context.Context, p Publication) error {
		if p.ID == "00000000000000002" {
			return &SendError{Status: 429, Backpressure: true, Detail: "busy"}
		}
		mu.Lock()
		defer mu.Unlock()
		if offline {
			queued = append(queued, p)
		} else {
			e.receive(Delivery{p.ID, 0, 1, 0, p.Payload})
		}
		return nil
	}
	a.reconnect = func(_ context.Context, _ []int) error {
		mu.Lock()
		defer mu.Unlock()
		offline = false
		for _, p := range queued {
			e.receive(Delivery{p.ID, 0, 1, 0, p.Payload})
		}
		return nil
	}
	r := e.run(context.Background(), a)
	if failed(r) || !r.Reconnect.Completed || r.Reconnect.PendingAtResume != 2 || r.Reconnect.Replay.Samples != 2 || r.PendingAfterDrain != 0 {
		t.Fatalf("incorrect reconnect accounting: %+v", r)
	}
}
func TestEngineDeliveryValidation(t *testing.T) {
	e := newEngine(testConfig())
	key := "00000000000000001"
	p := Publication{key, 0, 0, payload(key, 17)}
	e.items[key] = &trackedPublication{pub: p, start: time.Now(), seen: map[int]time.Time{}}
	e.receive(Delivery{key, 0, 0, 1, p.Payload})
	e.receive(Delivery{key, 0, 1, 1, []byte("corrupt")})
	e.receive(Delivery{"unknown", 0, 1, 1, p.Payload})
	e.receive(Delivery{key, 0, 1, 2, p.Payload})
	e.receive(Delivery{key, 0, 1, 2, p.Payload})
	key2 := "00000000000000002"
	p2 := Publication{key2, 0, 0, payload(key2, 17)}
	e.items[key2] = &trackedPublication{pub: p2, start: time.Now(), seen: map[int]time.Time{}}
	e.receive(Delivery{key2, 0, 1, 1, p2.Payload})
	i := e.result.Integrity
	if i.Unexpected != 2 || i.Corrupt != 1 || i.Duplicate != 1 || i.OutOfOrder != 1 {
		t.Fatalf("validation counters: %+v", i)
	}
}
func TestQuantiles(t *testing.T) {
	q := quantiles([]time.Duration{100 * time.Millisecond, time.Millisecond, 50 * time.Millisecond})
	if q.Samples != 3 || q.P50 != 50 || q.P99 != 100 {
		t.Fatalf("quantiles: %+v", q)
	}
	if quantiles(nil).Samples != 0 {
		t.Fatal("empty quantiles")
	}
}

var calibrationPayload []byte

func BenchmarkPayloadCalibration(b *testing.B) {
	for i := 0; i < b.N; i++ {
		calibrationPayload = payload("00000000000000001", 256)
	}
}

// Measures generator accounting only; it does not estimate server capacity.
func BenchmarkEngineAccountingCalibration(b *testing.B) {
	e := newEngine(testConfig())
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		id := fmt.Sprintf("%017d", i)
		p := Publication{id, 0, 0, payload(id, 256)}
		e.items[id] = &trackedPublication{pub: p, start: time.Now(), seen: map[int]time.Time{}}
		e.receive(Delivery{id, 0, 1, uint64(i + 1), p.Payload})
		delete(e.items, id)
	}
}

func TestQuantilesNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for i := range values {
		values[i] = time.Duration(i+1) * time.Millisecond
	}
	q := quantiles(values)
	if q.P50 != 50 || q.P95 != 95 || q.P99 != 99 {
		t.Fatalf("nearest-rank percentile: %+v", q)
	}
	q = quantiles([]time.Duration{time.Millisecond, 2 * time.Millisecond})
	if q.P50 != 1 || q.P99 != 2 {
		t.Fatalf("two-element ranks: %+v", q)
	}
	q = quantiles([]time.Duration{7 * time.Millisecond})
	if q.P50 != 7 || q.P95 != 7 || q.P99 != 7 {
		t.Fatal(q)
	}
}

func TestSendErrorEvidenceIsBoundedAndRedacted(t *testing.T) {
	e := newEngine(testConfig())
	for i := 0; i < 30; i++ {
		e.recordSendError(&SendError{Status: i, Detail: "Authorization: secret-value https://host/?ticket=secret-value"})
	}
	e.recordSendError(fmt.Errorf("request https://host/?ticket=secret-value: %w", context.DeadlineExceeded))
	if len(e.result.SendErrorCounts) > 10 {
		t.Fatalf("unbounded reasons: %v", e.result.SendErrorCounts)
	}
	total := 0
	for _, n := range e.result.SendErrorCounts {
		total += n
	}
	if total != 31 {
		t.Fatalf("error counts lost: %v", e.result.SendErrorCounts)
	}
	encoded, _ := json.Marshal(e.result.SendErrorCounts)
	if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "https") {
		t.Fatal("error detail leaked")
	}
	if got := sendErrorReason(errors.New("invalid QGramm acknowledgement identity")); got != "uncertain_qgramm_ack_identity" {
		t.Fatal(got)
	}
	if got := sendErrorReason(errors.New("arbitrary private response secret-value")); got != "uncertain_other" {
		t.Fatal(got)
	}
}

func TestSendErrorEvidenceCountsActualOutcomes(t *testing.T) {
	for i := 0; i < 2; i++ {
		e := newEngine(testConfig())
		a := &testAdapter{send: func(context.Context, Publication) error { return errors.New("invalid QGramm receipt") }}
		r := e.run(context.Background(), a)
		if r.SendErrorCounts["uncertain_invalid_qgramm_receipt"] != 3 || r.Phases[0].Uncertain != 3 || !failed(r) {
			t.Fatalf("opaque uncertain outcome: %+v", r)
		}
	}
}
