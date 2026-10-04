package main

import (
	"testing"
	"time"
)

func TestNearestRank(t *testing.T) {
	q := quantiles([]float64{10, 1, 9, 2, 8, 3, 7, 4, 6, 5})
	if q["p50"] != 5 || q["p95"] != 10 || q["p99"] != 10 {
		t.Fatalf("wrong nearest ranks: %v", q)
	}
	if len(quantiles(nil)) != 0 {
		t.Fatal("no samples must not fabricate percentiles")
	}
}
func TestObservedDeliveryIntegrity(t *testing.T) {
	entries = map[string]*entry{"00000000000000001": {started: time.Now(), phase: 0}}
	phases = make([]phase, 1)
	deliverySamples = make([][]float64, 1)
	connectionErrors.Store(0)
	observe("00000000000000001")
	observe("00000000000000001")
	observe("unknown")
	if phases[0].Delivered != 1 || phases[0].Duplicate != 1 || len(deliverySamples[0]) != 1 || connectionErrors.Load() != 1 {
		t.Fatal("delivery integrity counters")
	}
}

func TestValidationRejectsLossAndErrors(t *testing.T) {
	good := phase{Name: "steady", Offered: 5, Accepted: 5, Delivered: 5}
	if err := validateResults([]phase{good}, 0); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []phase{{Offered: 5, Accepted: 4, Delivered: 4}, {Offered: 5, Accepted: 5, Delivered: 4}, {Offered: 5, Accepted: 5, Delivered: 5, Duplicate: 1}, {Offered: 5, Accepted: 5, Delivered: 5, PublishErrors: 1}} {
		if validateResults([]phase{bad}, 0) == nil {
			t.Fatal("invalid evidence passed")
		}
	}
	if validateResults([]phase{good}, 1) == nil {
		t.Fatal("receive errors passed")
	}
}
