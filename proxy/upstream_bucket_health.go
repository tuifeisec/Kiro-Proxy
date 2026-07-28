package proxy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"kiro-proxy/store"
)

const (
	upstreamBucketFailureWindow = 30 * time.Second
	upstreamBucketOpenTTL       = 30 * time.Second
	upstreamBucketHalfOpenTTL   = 30 * time.Second
	upstreamBucketIdleTTL       = 10 * time.Minute
	upstreamBucketCapacity      = 256
)

type upstreamBucketHealthConfig struct {
	failureWindow time.Duration
	openTTL       time.Duration
	halfOpenTTL   time.Duration
	idleTTL       time.Duration
	capacity      int
}

type upstreamBucketState struct {
	failures         map[string]time.Time
	openUntil        time.Time
	halfOpenInFlight bool
	halfOpenUntil    time.Time
	lastTouched      time.Time
}

type upstreamBucketHealth struct {
	mu      sync.Mutex
	config  upstreamBucketHealthConfig
	buckets map[string]*upstreamBucketState
}

func newUpstreamBucketHealth() *upstreamBucketHealth {
	return newUpstreamBucketHealthWithConfig(upstreamBucketHealthConfig{})
}

func newUpstreamBucketHealthWithConfig(config upstreamBucketHealthConfig) *upstreamBucketHealth {
	if config.failureWindow <= 0 {
		config.failureWindow = upstreamBucketFailureWindow
	}
	if config.openTTL <= 0 {
		config.openTTL = upstreamBucketOpenTTL
	}
	if config.halfOpenTTL <= 0 {
		config.halfOpenTTL = upstreamBucketHalfOpenTTL
	}
	if config.idleTTL <= 0 {
		config.idleTTL = upstreamBucketIdleTTL
	}
	if config.capacity <= 0 {
		config.capacity = upstreamBucketCapacity
	}
	return &upstreamBucketHealth{
		config:  config,
		buckets: make(map[string]*upstreamBucketState),
	}
}

// order 保持同类候选的原顺序，仅将正在回避的上游地址桶移到末尾。
func (h *upstreamBucketHealth) order(candidates []*store.Model, now time.Time) []*store.Model {
	if h == nil || len(candidates) < 2 {
		return candidates
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(now)

	ordered := make([]*store.Model, 0, len(candidates))
	avoided := make([]*store.Model, 0, len(candidates))
	for _, candidate := range candidates {
		if h.avoidedLocked(upstreamBucketKeyForModel(candidate), now) {
			avoided = append(avoided, candidate)
		} else {
			ordered = append(ordered, candidate)
		}
	}
	return append(ordered, avoided...)
}

// allow 在桶过期后只放行一个半开探测，探测租约过期后可重试。
func (h *upstreamBucketHealth) allow(model *store.Model, now time.Time) (bool, string) {
	if h == nil {
		return true, "closed"
	}
	key := upstreamBucketKeyForModel(model)
	if key == "" {
		return true, "closed"
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(now)
	state := h.buckets[key]
	if state == nil || state.openUntil.IsZero() {
		return true, "closed"
	}
	state.lastTouched = now
	if now.Before(state.openUntil) {
		return false, "open"
	}
	if state.halfOpenInFlight && now.Before(state.halfOpenUntil) {
		return false, "half-open-busy"
	}
	state.halfOpenInFlight = true
	state.halfOpenUntil = now.Add(h.config.halfOpenTTL)
	return true, "half-open"
}

// recordFailure 只由调用方在 transport 类失败时调用。
func (h *upstreamBucketHealth) recordFailure(model *store.Model, now time.Time) {
	if h == nil || model == nil || strings.TrimSpace(model.ID) == "" {
		return
	}
	key := upstreamBucketKeyForModel(model)
	if key == "" {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(now)
	state := h.buckets[key]
	if state == nil {
		h.ensureCapacityLocked()
		state = &upstreamBucketState{failures: make(map[string]time.Time)}
		h.buckets[key] = state
	}
	h.pruneFailuresLocked(state, now)
	state.failures[model.ID] = now
	state.halfOpenInFlight = false
	state.halfOpenUntil = time.Time{}
	state.lastTouched = now
	if len(state.failures) >= 2 {
		state.openUntil = now.Add(h.config.openTTL)
	}
}

func (h *upstreamBucketHealth) recordSuccess(model *store.Model, now time.Time) {
	if h == nil {
		return
	}
	key := upstreamBucketKeyForModel(model)
	if key == "" {
		return
	}
	h.mu.Lock()
	delete(h.buckets, key)
	h.cleanupLocked(now)
	h.mu.Unlock()
}

func (h *upstreamBucketHealth) neutral(model *store.Model, now time.Time) {
	if h == nil {
		return
	}
	key := upstreamBucketKeyForModel(model)
	if key == "" {
		return
	}
	h.mu.Lock()
	if state := h.buckets[key]; state != nil {
		state.halfOpenInFlight = false
		state.halfOpenUntil = time.Time{}
		state.lastTouched = now
	}
	h.cleanupLocked(now)
	h.mu.Unlock()
}

func (h *upstreamBucketHealth) state(model *store.Model, now time.Time) string {
	if h == nil {
		return "closed"
	}
	key := upstreamBucketKeyForModel(model)
	if key == "" {
		return "closed"
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(now)
	state := h.buckets[key]
	if state == nil || state.openUntil.IsZero() {
		return "closed"
	}
	if now.Before(state.openUntil) {
		return "open"
	}
	if state.halfOpenInFlight && now.Before(state.halfOpenUntil) {
		return "half-open-busy"
	}
	return "half-open"
}

func isUpstreamTransportFailure(err error) bool {
	if err == nil || errors.Is(err, errFirstByteTimeout) || errors.Is(err, context.Canceled) {
		return false
	}
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (h *upstreamBucketHealth) avoidedLocked(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	state := h.buckets[key]
	if state == nil || state.openUntil.IsZero() {
		return false
	}
	if now.Before(state.openUntil) {
		return true
	}
	return state.halfOpenInFlight && now.Before(state.halfOpenUntil)
}

func (h *upstreamBucketHealth) cleanupLocked(now time.Time) {
	for key, state := range h.buckets {
		h.pruneFailuresLocked(state, now)
		if state.halfOpenInFlight && !now.Before(state.halfOpenUntil) {
			state.halfOpenInFlight = false
			state.halfOpenUntil = time.Time{}
		}
		if state.openUntil.IsZero() && len(state.failures) == 0 {
			delete(h.buckets, key)
			continue
		}
		expiresAfter := state.lastTouched.Add(h.config.idleTTL)
		if !state.openUntil.IsZero() {
			expiresAfter = state.openUntil.Add(h.config.idleTTL)
		}
		if !now.Before(expiresAfter) {
			delete(h.buckets, key)
		}
	}
}

func (h *upstreamBucketHealth) pruneFailuresLocked(state *upstreamBucketState, now time.Time) {
	if state == nil {
		return
	}
	cutoff := now.Add(-h.config.failureWindow)
	for modelID, failedAt := range state.failures {
		if failedAt.Before(cutoff) {
			delete(state.failures, modelID)
		}
	}
}

func (h *upstreamBucketHealth) ensureCapacityLocked() {
	if len(h.buckets) < h.config.capacity {
		return
	}
	oldestKey := ""
	var oldestTouched time.Time
	for key, state := range h.buckets {
		if oldestKey == "" || state.lastTouched.Before(oldestTouched) {
			oldestKey = key
			oldestTouched = state.lastTouched
		}
	}
	delete(h.buckets, oldestKey)
}

func upstreamBucketKeyForModel(model *store.Model) string {
	if model == nil {
		return ""
	}
	return upstreamBucketKey(model.BaseURL, model.ProxyURL)
}

func upstreamBucketKey(baseURL, proxyURL string) string {
	origin := normalizedURLOrigin(baseURL)
	if origin == "" {
		return ""
	}
	proxyIdentity := normalizedProxyURL(proxyURL)
	return origin + "\x00" + proxyIdentity
}

func normalizedURLOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return strings.ToLower(parsed.Scheme) + "://" + normalizedURLHost(parsed)
}

func normalizedProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "direct"
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = normalizedURLHost(parsed)
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String()
}

func normalizedURLHost(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (strings.EqualFold(parsed.Scheme, "https") && port == "443") ||
		(strings.EqualFold(parsed.Scheme, "http") && port == "80") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
