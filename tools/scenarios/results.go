package main

import (
	"math"
	"sort"
	"time"
)

type Quantiles struct {
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	P99     float64 `json:"p99_ms"`
	Samples int     `json:"samples"`
}

func quantiles(samples []time.Duration) Quantiles {
	v := append([]time.Duration(nil), samples...)
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	q := Quantiles{Samples: len(v)}
	if len(v) == 0 {
		return q
	}
	pick := func(p float64) float64 {
		i := int(math.Ceil(float64(len(v))*p)) - 1
		if i >= len(v) {
			i = len(v) - 1
		}
		return float64(v[i]) / float64(time.Millisecond)
	}
	q.P50 = pick(.5)
	q.P95 = pick(.95)
	q.P99 = pick(.99)
	return q
}

type PhaseResult struct {
	Name              string    `json:"name"`
	TargetRate        int       `json:"target_messages_per_second"`
	DurationSeconds   float64   `json:"duration_seconds"`
	Planned           int       `json:"planned"`
	Scheduled         int       `json:"scheduled"`
	Offered           int       `json:"offered"`
	Accepted          int       `json:"accepted"`
	Rejected          int       `json:"rejected"`
	Backpressure      int       `json:"backpressure"`
	Uncertain         int       `json:"uncertain"`
	Skipped           int       `json:"skipped"`
	Delivered         int       `json:"delivered"`
	Duplicate         int       `json:"duplicate"`
	ActualOfferedRate float64   `json:"actual_offered_rate"`
	ACK               Quantiles `json:"ack"`
	Delivery          Quantiles `json:"delivery"`
	GeneratorLate     Quantiles `json:"generator_late"`
	SLOPass           bool      `json:"slo_pass"`
}
type OutstandingSample struct {
	Seconds  float64 `json:"seconds"`
	Pending  int     `json:"pending_deliveries"`
	MaxAgeMS float64 `json:"max_age_ms"`
}
type IntegrityResult struct {
	Accepted           int `json:"accepted"`
	ExpectedDeliveries int `json:"expected_deliveries"`
	Received           int `json:"received"`
	Missing            int `json:"missing"`
	Duplicate          int `json:"duplicate"`
	Unexpected         int `json:"unexpected"`
	Corrupt            int `json:"corrupt"`
	OutOfOrder         int `json:"out_of_order"`
	AdapterErrors      int `json:"adapter_errors"`
}
type ReconnectResult struct {
	Receivers       int       `json:"receivers"`
	OfflineSeconds  float64   `json:"offline_seconds"`
	Replay          Quantiles `json:"replay_origin_latency"`
	DrainMS         float64   `json:"resume_drain_ms"`
	PendingAtResume int       `json:"pending_at_resume"`
	Completed       bool      `json:"completed"`
}
type Results struct {
	Service              string              `json:"service"`
	Users                int                 `json:"users"`
	Chats                int                 `json:"chats"`
	Fanout               int                 `json:"fanout"`
	PayloadBytes         int                 `json:"payload_bytes"`
	Workers              int                 `json:"workers"`
	IntegrityPassed      bool                `json:"integrity_passed"`
	LoadPass             bool                `json:"load_pass"`
	StartedAt            time.Time           `json:"started_at"`
	Phases               []PhaseResult       `json:"phases"`
	Outstanding          []OutstandingSample `json:"outstanding"`
	Integrity            IntegrityResult     `json:"integrity"`
	History              HistoryResult       `json:"history"`
	Reconnect            ReconnectResult     `json:"reconnect"`
	HighestTestedSLORate int                 `json:"highest_tested_slo_rate"`
	DrainSeconds         float64             `json:"drain_seconds"`
	PendingAfterDrain    int                 `json:"pending_after_drain"`
	Error                string              `json:"error,omitempty"`
	SendErrorCounts      map[string]int      `json:"send_error_counts,omitempty"`
	DuplicatePolicy      string              `json:"duplicate_policy"`
}
