package proxy

import (
	"fmt"
	"kiro-proxy/store"
	"testing"
	"time"
)

func TestModelLatencyTrackerKeepsRecentSamplesAndCalculatesMetrics(t *testing.T) {
	tracker := newModelLatencyTracker()
	now := time.Now()
	for value := 1; value <= modelLatencySampleLimit+1; value++ {
		tracker.record("model-1", time.Duration(value)*time.Millisecond, time.Second, now.Add(time.Duration(value)*time.Millisecond))
	}

	metric := tracker.snapshot(now.Add(time.Second))["model-1"]
	if metric.sampleCount != modelLatencySampleLimit {
		t.Fatalf("expected %d samples, got %d", modelLatencySampleLimit, metric.sampleCount)
	}
	if metric.lastMS != 65 || metric.averageMS != 33 || metric.p95MS != 62 {
		t.Fatalf("unexpected latency metric: %+v", metric)
	}
	if metric.ewmaMS <= metric.averageMS || metric.ewmaMS >= metric.lastMS {
		t.Fatalf("unexpected EWMA: %+v", metric)
	}
}

func TestModelLatencyTrackerOrdersDegradedCandidatesLast(t *testing.T) {
	tracker := newModelLatencyTracker()
	now := time.Now()
	threshold := 100 * time.Millisecond
	tracker.record("slow", threshold, threshold, now)
	tracker.record("slow", threshold, threshold, now.Add(time.Second))
	candidates := []*store.Model{{ID: "slow"}, {ID: "healthy"}}

	ordered := tracker.order(candidates, now.Add(2*time.Second))
	if ordered[0].ID != "healthy" || ordered[1].ID != "slow" {
		t.Fatalf("degraded candidate was not moved last: %+v", ordered)
	}

	tracker.record("healthy", threshold, threshold, now.Add(2*time.Second))
	tracker.record("healthy", threshold, threshold, now.Add(3*time.Second))
	ordered = tracker.order(candidates, now.Add(4*time.Second))
	if ordered[0].ID != "slow" || ordered[1].ID != "healthy" {
		t.Fatalf("all-degraded ordering should stay stable: %+v", ordered)
	}
}

func TestModelLatencyTrackerDegradesAndRecovers(t *testing.T) {
	tracker := newModelLatencyTracker()
	now := time.Now()
	threshold := 100 * time.Millisecond
	tracker.record("model-1", threshold, threshold, now)
	if tracker.isDegraded("model-1", now) {
		t.Fatal("single slow sample should not degrade model")
	}
	tracker.record("model-1", 120*time.Millisecond, threshold, now.Add(10*time.Second))
	if !tracker.isDegraded("model-1", now.Add(10*time.Second)) {
		t.Fatal("two consecutive slow samples should degrade model")
	}

	tracker.record("model-1", 50*time.Millisecond, threshold, now.Add(20*time.Second))
	if !tracker.isDegraded("model-1", now.Add(20*time.Second)) {
		t.Fatal("one healthy sample should not clear degradation")
	}
	tracker.record("model-1", 40*time.Millisecond, threshold, now.Add(30*time.Second))
	if tracker.isDegraded("model-1", now.Add(30*time.Second)) {
		t.Fatal("two consecutive healthy samples should clear degradation")
	}

	tracker.record("model-2", threshold, threshold, now)
	tracker.record("model-2", threshold, threshold, now.Add(modelLatencySlowWindow+time.Second))
	if tracker.isDegraded("model-2", now.Add(modelLatencySlowWindow+time.Second)) {
		t.Fatal("slow samples outside the slow window should not degrade model")
	}
}

func TestModelLatencyTrackerExpiresAndCapsState(t *testing.T) {
	tracker := newModelLatencyTracker()
	now := time.Now()
	for index := 0; index <= modelLatencyMaxModels; index++ {
		tracker.record(fmt.Sprintf("model-%d", index), time.Millisecond, time.Second, now.Add(time.Duration(index)*time.Nanosecond))
	}
	if len(tracker.states) != modelLatencyMaxModels {
		t.Fatalf("expected capacity %d, got %d", modelLatencyMaxModels, len(tracker.states))
	}
	if _, exists := tracker.states["model-0"]; exists {
		t.Fatal("oldest model state was not evicted")
	}

	if snapshot := tracker.snapshot(now.Add(modelLatencyStateTTL + time.Second)); len(snapshot) != 0 {
		t.Fatalf("expected expired tracker state to be removed, got %d models", len(snapshot))
	}
}
