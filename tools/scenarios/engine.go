package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type trackedPublication struct {
	pub      Publication
	start    time.Time
	phase    int
	accepted bool
	outcome  bool
	seen     map[int]time.Time
}
type phaseData struct {
	result              PhaseResult
	ack, delivery, late []time.Duration
}
type Engine struct {
	cfg           Config
	mu            sync.Mutex
	items         map[string]*trackedPublication
	phases        []phaseData
	result        Results
	started       time.Time
	seq           map[int]uint64
	offline       map[int]bool
	replay        []time.Duration
	resume        time.Time
	resumeIDs     map[string]map[int]bool
	resumePending int
}

// Only fixed classifications are exported. Never persist an arbitrary SDK
// error: it may contain a URL, ticket, authorization header or response body.
func sendErrorReason(err error) string {
	var reject *SendError
	if errors.As(err, &reject) {
		return fmt.Sprintf("rejected_status_%d", reject.Status)
	}
	if errors.Is(err, context.Canceled) {
		return "uncertain_context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "uncertain_deadline_exceeded"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "uncertain_network_timeout"
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return "uncertain_invalid_json"
	}
	for _, known := range []struct{ text, reason string }{
		{"invalid QGramm receipt", "uncertain_invalid_qgramm_receipt"},
		{"invalid QGramm message ACK", "uncertain_invalid_qgramm_ack"},
		{"invalid QGramm acknowledgement identity", "uncertain_qgramm_ack_identity"},
		{"invalid QGramm publication topology", "uncertain_qgramm_topology"},
		{"message authentication failed", "uncertain_cipher_authentication"},
		{"sender disconnected", "uncertain_sender_disconnected"},
		{"websocket closed", "uncertain_websocket_closed"},
	} {
		if strings.Contains(err.Error(), known.text) {
			return known.reason
		}
	}
	return "uncertain_other"
}

func (e *Engine) recordSendError(err error) {
	if e.result.SendErrorCounts == nil {
		e.result.SendErrorCounts = map[string]int{}
	}
	reason := sendErrorReason(err)
	if _, exists := e.result.SendErrorCounts[reason]; !exists && len(e.result.SendErrorCounts) >= 9 {
		reason = "other"
	}
	e.result.SendErrorCounts[reason]++
}

func newEngine(c Config) *Engine {
	return &Engine{cfg: c, items: map[string]*trackedPublication{}, seq: map[int]uint64{}, offline: map[int]bool{}, result: Results{Service: c.Service, Users: c.Users, Chats: c.Chats, Fanout: c.Fanout, PayloadBytes: c.PayloadBytes, Workers: c.Workers, DuplicatePolicy: "unique delivery integrity allows counted at-least-once duplicates; strict load SLO requires zero"}, resumeIDs: map[string]map[int]bool{}}
}
func (e *Engine) receive(d Delivery) {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	t, ok := e.items[d.ID]
	if !ok || !isReceiver(e.cfg, d.Receiver) || d.Chat != chatFor(e.cfg, d.Receiver) || d.Chat != t.pub.Chat {
		e.result.Integrity.Unexpected++
		return
	}
	if !bytes.Equal(t.pub.Payload, d.Payload) {
		e.result.Integrity.Corrupt++
		return
	}
	if _, ok := t.seen[d.Receiver]; ok {
		e.result.Integrity.Duplicate++
		if t.phase < len(e.phases) {
			e.phases[t.phase].result.Duplicate++
		}
		return
	}
	if d.Seq > 0 {
		if d.Seq <= e.seq[d.Receiver] {
			e.result.Integrity.OutOfOrder++
		}
		if d.Seq > e.seq[d.Receiver] {
			e.seq[d.Receiver] = d.Seq
		}
	}
	t.seen[d.Receiver] = now
	if recipients, ok := e.resumeIDs[d.ID]; ok && recipients[d.Receiver] {
		e.replay = append(e.replay, now.Sub(t.start))
		delete(recipients, d.Receiver)
		e.resumePending--
		if e.resumePending == 0 {
			e.result.Reconnect.Completed = true
			e.result.Reconnect.DrainMS = float64(now.Sub(e.resume)) / float64(time.Millisecond)
		}
	}
}
func (e *Engine) phase(name string) {
	if e.cfg.PhaseFile != "" {
		_ = os.WriteFile(e.cfg.PhaseFile, []byte(name), 0600)
	}
}
func (e *Engine) pendingLocked(now time.Time) (int, time.Duration) {
	n := 0
	var age time.Duration
	for _, t := range e.items {
		if !t.accepted {
			continue
		}
		n += e.cfg.Fanout - len(t.seen)
		if len(t.seen) < e.cfg.Fanout && now.Sub(t.start) > age {
			age = now.Sub(t.start)
		}
	}
	return n, age
}
func (e *Engine) sample() {
	now := time.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	n, age := e.pendingLocked(now)
	e.result.Outstanding = append(e.result.Outstanding, OutstandingSample{now.Sub(e.started).Seconds(), n, float64(age) / float64(time.Millisecond)})
}
func payload(id string, size int) []byte { b := bytes.Repeat([]byte{'x'}, size); copy(b, id); return b }
func waitUntil(ctx context.Context, deadline time.Time) bool {
	d := time.Until(deadline)
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
func (e *Engine) run(ctx context.Context, a Adapter) Results {
	e.started = time.Now()
	e.result.StartedAt = e.started
	stopSampler := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				e.sample()
			case <-stopSampler:
				return
			}
		}
	}()
	e.phase("idle")
	waitUntil(ctx, time.Now().Add(e.cfg.Idle))
	sendRoot, cancelSends := context.WithCancel(ctx)
	defer cancelSends()
	sem := make(chan struct{}, e.cfg.Workers)
	var sends sync.WaitGroup
	var id uint64
	var reconnectWait sync.WaitGroup
	for pi, step := range e.cfg.Steps {
		e.mu.Lock()
		e.phases = append(e.phases, phaseData{result: PhaseResult{Name: step.Name, TargetRate: step.Rate, DurationSeconds: step.Duration.Seconds()}})
		e.mu.Unlock()
		e.phase(step.Name)
		start := time.Now()
		end := start.Add(step.Duration)
		if pi == 1 && e.cfg.ReconnectCount > 0 {
			ids := []int{}
			for r := 0; r < e.cfg.Chats*(e.cfg.Fanout+1) && len(ids) < e.cfg.ReconnectCount; r++ {
				if isReceiver(e.cfg, r) {
					ids = append(ids, r)
				}
			}
			if err := a.Disconnect(ctx, ids); err != nil {
				e.result.Error = "disconnect: " + err.Error()
			} else {
				e.mu.Lock()
				for _, r := range ids {
					e.offline[r] = true
				}
				e.result.Reconnect.Receivers = len(ids)
				e.mu.Unlock()
				offline := step.Duration
				if e.cfg.Offline > 0 && e.cfg.Offline < offline {
					offline = e.cfg.Offline
				}
				e.result.Reconnect.OfflineSeconds = offline.Seconds()
				reconnectWait.Add(1)
				go func() {
					defer reconnectWait.Done()
					if !waitUntil(ctx, start.Add(offline)) {
						return
					}
					e.mu.Lock()
					e.resume = time.Now()
					for key, t := range e.items {
						if t.outcome && !t.accepted {
							continue
						}
						for _, r := range ids {
							if t.pub.Chat == chatFor(e.cfg, r) {
								if _, ok := t.seen[r]; !ok {
									if e.resumeIDs[key] == nil {
										e.resumeIDs[key] = map[int]bool{}
									}
									e.resumeIDs[key][r] = true
									e.resumePending++
								}
							}
						}
					}
					e.result.Reconnect.PendingAtResume = e.resumePending
					if e.resumePending == 0 {
						e.result.Reconnect.Completed = true
					}
					e.mu.Unlock()
					if err := a.Reconnect(ctx, ids); err != nil {
						e.mu.Lock()
						e.result.Error = "reconnect: " + err.Error()
						e.mu.Unlock()
					}
				}()
			}
		}
		planned := int(step.Duration.Seconds() * float64(step.Rate))
		e.mu.Lock()
		e.phases[pi].result.Planned = planned
		e.mu.Unlock()
		for tick := 0; tick < planned; tick++ {
			target := start.Add(time.Duration(float64(time.Second) * float64(tick) / float64(step.Rate)))
			if !waitUntil(ctx, target) {
				break
			}
			now := time.Now()
			late := now.Sub(target)
			e.mu.Lock()
			p := &e.phases[pi]
			p.result.Scheduled++
			p.late = append(p.late, late)
			e.mu.Unlock()
			// Do not turn a stalled generator into a catch-up burst.
			if now.After(end) || late > max(5*time.Millisecond, 2*time.Second/time.Duration(step.Rate)) {
				e.mu.Lock()
				e.phases[pi].result.Skipped++
				e.mu.Unlock()
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				e.mu.Lock()
				e.phases[pi].result.Skipped++
				e.mu.Unlock()
				continue
			}
			id++
			key := fmt.Sprintf("%017d", id)
			chat := int((id - 1) % uint64(e.cfg.Chats))
			pub := Publication{key, chat, senderFor(e.cfg, chat), payload(key, e.cfg.PayloadBytes)}
			e.mu.Lock()
			e.items[key] = &trackedPublication{pub: pub, start: now, phase: pi, seen: map[int]time.Time{}}
			e.phases[pi].result.Offered++
			e.mu.Unlock()
			sends.Add(1)
			go func(key string, pub Publication, pi int, started time.Time) {
				defer sends.Done()
				defer func() { <-sem }()
				sendCtx, cancel := context.WithTimeout(sendRoot, 30*time.Second)
				defer cancel()
				err := a.Send(sendCtx, pub)
				finished := time.Now()
				e.mu.Lock()
				defer e.mu.Unlock()
				t := e.items[key]
				t.outcome = true
				if err == nil {
					t.accepted = true
					e.phases[pi].result.Accepted++
					e.phases[pi].ack = append(e.phases[pi].ack, finished.Sub(started))
				} else {
					e.recordSendError(err)
					var rejected *SendError
					if errors.As(err, &rejected) {
						e.phases[pi].result.Rejected++
						if pending := e.resumeIDs[key]; pending != nil {
							e.resumePending -= len(pending)
							delete(e.resumeIDs, key)
						}
						if rejected.Backpressure {
							e.phases[pi].result.Backpressure++
						}
					} else {
						e.phases[pi].result.Uncertain++
					}
				}
			}(key, pub, pi, now)
		}
		waitUntil(ctx, end)
	}
	e.phase("drain")
	drainStart := time.Now()
	drainTimer := time.AfterFunc(e.cfg.Drain, cancelSends)
	sends.Wait()
	drainTimer.Stop()
	reconnectWait.Wait()
	for time.Since(drainStart) < e.cfg.Drain {
		e.mu.Lock()
		n, _ := e.pendingLocked(time.Now())
		e.mu.Unlock()
		if n == 0 {
			break
		}
		if !waitUntil(ctx, time.Now().Add(25*time.Millisecond)) {
			break
		}
	}
	e.result.DrainSeconds = time.Since(drainStart).Seconds()
	e.mu.Lock()
	e.result.PendingAfterDrain, _ = e.pendingLocked(time.Now())
	e.mu.Unlock()
	e.phase("history")
	e.mu.Lock()
	accepted := []Publication{}
	for _, t := range e.items {
		if t.accepted {
			accepted = append(accepted, t.pub)
		}
	}
	e.mu.Unlock()
	// History verification is outside workload timing and scans every chat.
	historyCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	h, err := a.History(historyCtx, accepted)
	cancel()
	if err != nil {
		e.result.Error = "history: " + err.Error()
	}
	e.result.History = h
	close(stopSampler)
	sampler.Wait()
	e.sample()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, t := range e.items {
		if !t.accepted {
			continue
		}
		e.result.Integrity.Accepted++
		e.result.Integrity.ExpectedDeliveries += e.cfg.Fanout
		e.result.Integrity.Received += len(t.seen)
		e.result.Integrity.Missing += e.cfg.Fanout - len(t.seen)
		p := &e.phases[t.phase]
		p.result.Delivered += len(t.seen)
		for _, at := range t.seen {
			p.delivery = append(p.delivery, at.Sub(t.start))
		}
	}
	e.result.Integrity.AdapterErrors = int(a.Errors())
	e.result.Reconnect.Replay = quantiles(e.replay)
	for _, p := range e.phases {
		p.result.ACK = quantiles(p.ack)
		p.result.Delivery = quantiles(p.delivery)
		p.result.GeneratorLate = quantiles(p.late)
		p.result.ActualOfferedRate = float64(p.result.Offered) / p.result.DurationSeconds
		p.result.SLOPass = p.result.Delivery.P99 <= 250 && p.result.Delivered == p.result.Accepted*e.cfg.Fanout && p.result.Skipped == 0 && p.result.Uncertain == 0 && p.result.Rejected == 0 && p.result.Accepted == p.result.Planned && p.result.Duplicate == 0
		if p.result.SLOPass && p.result.TargetRate > e.result.HighestTestedSLORate {
			e.result.HighestTestedSLORate = p.result.TargetRate
		}
		e.result.Phases = append(e.result.Phases, p.result)
	}
	e.result.IntegrityPassed = !failed(e.result)
	e.result.LoadPass = e.result.IntegrityPassed && e.result.PendingAfterDrain == 0 && e.result.Integrity.Duplicate == 0
	for _, p := range e.result.Phases {
		if !p.SLOPass {
			e.result.LoadPass = false
		}
	}
	e.phase("done")
	return e.result
}
func failed(r Results) bool {
	i := r.Integrity
	if r.Error != "" || i.Missing+i.Unexpected+i.Corrupt+i.OutOfOrder+i.AdapterErrors > 0 || r.History.Missing+r.History.Corrupt+r.History.Unexpected > 0 {
		return true
	}
	for _, p := range r.Phases {
		if p.Uncertain > 0 {
			return true
		}
	}
	return false
}
