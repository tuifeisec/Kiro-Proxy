package proxy

import (
	"kiro-proxy/store"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	modelLatencySampleLimit             = 64
	modelLatencySlowWindow              = 60 * time.Second
	modelLatencyDegradedTTL             = 2 * time.Minute
	modelLatencyStateTTL                = 10 * time.Minute
	modelLatencyCleanupInterval         = time.Minute
	modelLatencyMaxModels               = 4096
	modelLatencyEWMAAlpha       float64 = 0.25
)

type modelLatencyMetric struct {
	lastMS          int64
	averageMS       int64
	ewmaMS          int64
	p95MS           int64
	sampleCount     int64
	slowSampleCount int
	degraded        bool
	degradedUntil   time.Time
}

type modelLatencyState struct {
	samples            []int64
	slowSamples        []time.Time
	consecutiveHealthy int
	degradedUntil      time.Time
	lastSeen           time.Time
}

type modelLatencyTracker struct {
	mu          sync.Mutex
	states      map[string]*modelLatencyState
	lastCleanup time.Time
}

func newModelLatencyTracker() *modelLatencyTracker {
	return &modelLatencyTracker{states: make(map[string]*modelLatencyState)}
}

func (t *modelLatencyTracker) record(modelID string, firstByte, threshold time.Duration, now time.Time) {
	if t == nil || firstByte < 0 {
		return
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(now, false)
	state := t.states[modelID]
	if state == nil {
		t.ensureCapacityLocked()
		state = &modelLatencyState{samples: make([]int64, 0, modelLatencySampleLimit)}
		t.states[modelID] = state
	}
	state.lastSeen = now
	state.samples = append(state.samples, firstByte.Milliseconds())
	if len(state.samples) > modelLatencySampleLimit {
		copy(state.samples, state.samples[len(state.samples)-modelLatencySampleLimit:])
		state.samples = state.samples[:modelLatencySampleLimit]
	}

	if threshold > 0 && firstByte >= threshold {
		state.consecutiveHealthy = 0
		state.slowSamples = pruneLatencyTimes(state.slowSamples, now.Add(-modelLatencySlowWindow))
		state.slowSamples = append(state.slowSamples, now)
		if len(state.slowSamples) >= 2 {
			state.degradedUntil = now.Add(modelLatencyDegradedTTL)
		}
		return
	}

	state.slowSamples = nil
	if now.Before(state.degradedUntil) {
		state.consecutiveHealthy++
		if state.consecutiveHealthy >= 2 {
			state.consecutiveHealthy = 0
			state.degradedUntil = time.Time{}
		}
		return
	}
	state.consecutiveHealthy = 0
	state.degradedUntil = time.Time{}
}

func (t *modelLatencyTracker) isDegraded(modelID string, now time.Time) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(now, false)
	state := t.states[strings.TrimSpace(modelID)]
	return state != nil && now.Before(state.degradedUntil)
}

func (t *modelLatencyTracker) order(candidates []*store.Model, now time.Time) []*store.Model {
	if t == nil || len(candidates) < 2 {
		return candidates
	}
	healthy := make([]*store.Model, 0, len(candidates))
	degraded := make([]*store.Model, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != nil && t.isDegraded(candidate.ID, now) {
			degraded = append(degraded, candidate)
		} else {
			healthy = append(healthy, candidate)
		}
	}
	if len(healthy) == 0 || len(degraded) == 0 {
		return candidates
	}
	return append(healthy, degraded...)
}

func (t *modelLatencyTracker) snapshot(now time.Time) map[string]modelLatencyMetric {
	result := make(map[string]modelLatencyMetric)
	if t == nil {
		return result
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanupLocked(now, true)
	for modelID, state := range t.states {
		state.slowSamples = pruneLatencyTimes(state.slowSamples, now.Add(-modelLatencySlowWindow))
		metric := calculateModelLatencyMetric(state.samples)
		metric.slowSampleCount = len(state.slowSamples)
		metric.degraded = now.Before(state.degradedUntil)
		if metric.degraded {
			metric.degradedUntil = state.degradedUntil
		}
		result[modelID] = metric
	}
	return result
}

func (t *modelLatencyTracker) cleanupLocked(now time.Time, force bool) {
	if !force && !t.lastCleanup.IsZero() && now.Sub(t.lastCleanup) < modelLatencyCleanupInterval {
		return
	}
	t.lastCleanup = now
	for modelID, state := range t.states {
		if !state.lastSeen.IsZero() && now.Sub(state.lastSeen) >= modelLatencyStateTTL {
			delete(t.states, modelID)
			continue
		}
		if !state.degradedUntil.IsZero() && !now.Before(state.degradedUntil) {
			state.degradedUntil = time.Time{}
			state.consecutiveHealthy = 0
		}
	}
}

func (t *modelLatencyTracker) ensureCapacityLocked() {
	if len(t.states) < modelLatencyMaxModels {
		return
	}
	oldestModelID := ""
	var oldestTime time.Time
	for modelID, state := range t.states {
		if oldestModelID == "" || state.lastSeen.Before(oldestTime) {
			oldestModelID = modelID
			oldestTime = state.lastSeen
		}
	}
	delete(t.states, oldestModelID)
}

func calculateModelLatencyMetric(samples []int64) modelLatencyMetric {
	if len(samples) == 0 {
		return modelLatencyMetric{}
	}
	total := int64(0)
	ewma := float64(samples[0])
	sortedSamples := append([]int64(nil), samples...)
	for index, sample := range samples {
		total += sample
		if index > 0 {
			ewma = modelLatencyEWMAAlpha*float64(sample) + (1-modelLatencyEWMAAlpha)*ewma
		}
	}
	sort.Slice(sortedSamples, func(i, j int) bool { return sortedSamples[i] < sortedSamples[j] })
	p95Index := int(math.Ceil(float64(len(sortedSamples))*0.95)) - 1
	return modelLatencyMetric{
		lastMS:      samples[len(samples)-1],
		averageMS:   total / int64(len(samples)),
		ewmaMS:      int64(math.Round(ewma)),
		p95MS:       sortedSamples[p95Index],
		sampleCount: int64(len(samples)),
	}
}

func pruneLatencyTimes(values []time.Time, cutoff time.Time) []time.Time {
	firstCurrent := 0
	for firstCurrent < len(values) && values[firstCurrent].Before(cutoff) {
		firstCurrent++
	}
	if firstCurrent == len(values) {
		return nil
	}
	return values[firstCurrent:]
}
