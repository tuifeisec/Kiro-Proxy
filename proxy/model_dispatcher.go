package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"kiro-proxy/router"
	"kiro-proxy/store"
)

const (
	modelFailureThreshold  = 3
	modelOpenDuration      = 60 * time.Second
	modelAffinityTTL       = 2 * time.Hour
	modelAvoidanceTTL      = 2 * time.Minute
	modelAvoidanceFailures = 2
)

var errFirstByteTimeout = errors.New("upstream first byte timeout")

type modelFailureClass string

const (
	modelFailureCredential modelFailureClass = "credential"
	modelFailureRateLimit  modelFailureClass = "rate_limit"
	modelFailureUpstream   modelFailureClass = "upstream"
	modelFailureTransport  modelFailureClass = "transport"
)

type modelCircuitState struct {
	consecutiveFailures int
	openUntil           time.Time
	halfOpenInFlight    bool
}

type modelHealthTracker struct {
	mu     sync.Mutex
	states map[string]*modelCircuitState
}

func newModelHealthTracker() *modelHealthTracker {
	return &modelHealthTracker{states: make(map[string]*modelCircuitState)}
}

func (h *modelHealthTracker) allow(model *store.Model, now time.Time) (bool, string) {
	if h == nil || model == nil {
		return true, "closed"
	}
	key := modelHealthKey(model)
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[key]
	if state == nil || state.openUntil.IsZero() {
		return true, "closed"
	}
	if now.Before(state.openUntil) {
		return false, "open"
	}
	if state.halfOpenInFlight {
		return false, "half-open-busy"
	}
	state.halfOpenInFlight = true
	return true, "half-open"
}

func (h *modelHealthTracker) success(model *store.Model) {
	if h == nil || model == nil {
		return
	}
	h.mu.Lock()
	delete(h.states, modelHealthKey(model))
	h.mu.Unlock()
}

func (h *modelHealthTracker) neutral(model *store.Model) {
	if h == nil || model == nil {
		return
	}
	h.mu.Lock()
	if state := h.states[modelHealthKey(model)]; state != nil {
		state.halfOpenInFlight = false
	}
	h.mu.Unlock()
}

func (h *modelHealthTracker) failure(model *store.Model, class modelFailureClass, retryAfter time.Duration, now time.Time) {
	if h == nil || model == nil {
		return
	}
	key := modelHealthKey(model)
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[key]
	if state == nil {
		state = &modelCircuitState{}
		h.states[key] = state
	}
	state.halfOpenInFlight = false
	state.consecutiveFailures++

	openDuration := time.Duration(0)
	switch class {
	case modelFailureCredential:
		openDuration = modelOpenDuration
	case modelFailureRateLimit:
		openDuration = retryAfter
		if openDuration <= 0 {
			openDuration = modelOpenDuration
		}
	case modelFailureUpstream, modelFailureTransport:
		if state.consecutiveFailures >= modelFailureThreshold {
			openDuration = modelOpenDuration
		}
	}
	if openDuration > 0 {
		state.openUntil = now.Add(openDuration)
	}
}

func (h *modelHealthTracker) snapshot(now time.Time) []store.ModelHealthStatus {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]store.ModelHealthStatus, 0, len(h.states))
	for key, state := range h.states {
		modelID := strings.SplitN(key, "|", 2)[0]
		status := store.ModelHealthStatus{
			ModelConfigID:       modelID,
			State:               "closed",
			ConsecutiveFailures: state.consecutiveFailures,
		}
		if !state.openUntil.IsZero() {
			status.OpenUntil = state.openUntil.Format(time.RFC3339Nano)
			if now.Before(state.openUntil) {
				status.State = "open"
			} else {
				status.State = "half-open"
			}
		}
		result = append(result, status)
	}
	return result
}

func (h *modelHealthTracker) syncModels(models []store.Model) {
	if h == nil {
		return
	}
	currentKeys := make(map[string]string, len(models))
	for i := range models {
		currentKeys[models[i].ID] = modelHealthKey(&models[i])
	}

	h.mu.Lock()
	for key := range h.states {
		modelID := strings.SplitN(key, "|", 2)[0]
		if currentKey, exists := currentKeys[modelID]; !exists || currentKey != key {
			delete(h.states, key)
		}
	}
	h.mu.Unlock()
}

func modelHealthKey(model *store.Model) string {
	if model == nil {
		return ""
	}
	return strings.Join([]string{model.ID, model.UpdatedAt}, "|")
}

type modelAffinityEntry struct {
	modelID   string
	expiresAt time.Time
}

type modelAffinityStore struct {
	mu      sync.Mutex
	entries map[string]modelAffinityEntry
}

type modelLoadTracker struct {
	mu              sync.Mutex
	active          map[string]int
	capacityChanged map[string]chan struct{}
}

type modelLease struct {
	tracker *modelLoadTracker
	modelID string
	once    sync.Once
}

func newModelLoadTracker() *modelLoadTracker {
	return &modelLoadTracker{
		active:          make(map[string]int),
		capacityChanged: make(map[string]chan struct{}),
	}
}

func (t *modelLoadTracker) acquire(modelID string, limit int) (*modelLease, bool) {
	return t.acquireWithWait(context.Background(), modelID, limit, 0)
}

func (t *modelLoadTracker) acquireWithWait(ctx context.Context, modelID string, limit int, waitDuration time.Duration) (*modelLease, bool) {
	if t == nil || modelID == "" {
		return &modelLease{}, true
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var timer *time.Timer
	var timeout <-chan time.Time
	if waitDuration > 0 {
		timer = time.NewTimer(waitDuration)
		timeout = timer.C
		defer timer.Stop()
	}

	for {
		t.mu.Lock()
		if limit <= 0 || t.active[modelID] < limit {
			t.active[modelID]++
			t.mu.Unlock()
			return &modelLease{tracker: t, modelID: modelID}, true
		}
		if waitDuration <= 0 {
			t.mu.Unlock()
			return nil, false
		}
		changed := t.capacityChanged[modelID]
		if changed == nil {
			changed = make(chan struct{})
			t.capacityChanged[modelID] = changed
		}
		t.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, false
		case <-timeout:
			return nil, false
		case <-changed:
		}
	}
}

func (l *modelLease) release() {
	if l == nil || l.tracker == nil || l.modelID == "" {
		return
	}
	l.once.Do(func() {
		l.tracker.mu.Lock()
		if l.tracker.active[l.modelID] <= 1 {
			delete(l.tracker.active, l.modelID)
		} else {
			l.tracker.active[l.modelID]--
		}
		if changed := l.tracker.capacityChanged[l.modelID]; changed != nil {
			close(changed)
			delete(l.tracker.capacityChanged, l.modelID)
		}
		l.tracker.mu.Unlock()
	})
}

func (t *modelLoadTracker) snapshot() map[string]int {
	result := make(map[string]int)
	if t == nil {
		return result
	}
	t.mu.Lock()
	for modelID, count := range t.active {
		result[modelID] = count
	}
	t.mu.Unlock()
	return result
}

type leasedUpstreamBody struct {
	io.ReadCloser
	lease *modelLease
}

func (b *leasedUpstreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.lease.release()
	}
	return n, err
}

func (b *leasedUpstreamBody) Close() error {
	err := b.ReadCloser.Close()
	b.lease.release()
	return err
}

type modelAvoidanceStore struct {
	mu      sync.Mutex
	entries map[string]modelAvoidanceEntry
}

type modelAvoidanceEntry struct {
	failures  int
	expiresAt time.Time
}

func newModelAvoidanceStore() *modelAvoidanceStore {
	return &modelAvoidanceStore{entries: make(map[string]modelAvoidanceEntry)}
}

func (s *modelAvoidanceStore) mark(scope, modelID string, now time.Time) {
	if s == nil || scope == "" || modelID == "" {
		return
	}
	s.mu.Lock()
	key := scope + "|" + modelID
	entry := s.entries[key]
	if !now.Before(entry.expiresAt) {
		entry.failures = 0
	}
	entry.failures++
	entry.expiresAt = now.Add(modelAvoidanceTTL)
	s.entries[key] = entry
	s.mu.Unlock()
}

func (s *modelAvoidanceStore) clear(scope, modelID string) {
	if s == nil || scope == "" || modelID == "" {
		return
	}
	s.mu.Lock()
	delete(s.entries, scope+"|"+modelID)
	s.mu.Unlock()
}

func (s *modelAvoidanceStore) avoided(scopes []string, modelID string, now time.Time) bool {
	if s == nil || modelID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, scope := range scopes {
		key := scope + "|" + modelID
		entry, ok := s.entries[key]
		if !ok {
			continue
		}
		if now.Before(entry.expiresAt) {
			if entry.failures >= modelAvoidanceFailures {
				return true
			}
			continue
		}
		delete(s.entries, key)
	}
	return false
}

func (s *modelAvoidanceStore) counts(now time.Time) map[string]int {
	result := make(map[string]int)
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if !now.Before(entry.expiresAt) {
			delete(s.entries, key)
			continue
		}
		if entry.failures < modelAvoidanceFailures {
			continue
		}
		parts := strings.Split(key, "|")
		if len(parts) > 1 {
			result[parts[len(parts)-1]]++
		}
	}
	return result
}

func newModelAffinityStore() *modelAffinityStore {
	return &modelAffinityStore{entries: make(map[string]modelAffinityEntry)}
}

func (s *modelAffinityStore) get(groupID, conversationID string, now time.Time) string {
	if s == nil || groupID == "" || conversationID == "" {
		return ""
	}
	key := groupID + "|" + conversationID
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[key]
	if !ok {
		return ""
	}
	if now.After(entry.expiresAt) {
		delete(s.entries, key)
		return ""
	}
	return entry.modelID
}

func (s *modelAffinityStore) set(groupID, conversationID, modelID string, now time.Time) {
	if s == nil || groupID == "" || conversationID == "" || modelID == "" {
		return
	}
	s.mu.Lock()
	s.entries[groupID+"|"+conversationID] = modelAffinityEntry{modelID: modelID, expiresAt: now.Add(modelAffinityTTL)}
	s.mu.Unlock()
}

func (s *modelAffinityStore) clear(groupID, conversationID, modelID string) {
	if s == nil || groupID == "" || conversationID == "" {
		return
	}
	key := groupID + "|" + conversationID
	s.mu.Lock()
	if entry, ok := s.entries[key]; ok && (modelID == "" || entry.modelID == modelID) {
		delete(s.entries, key)
	}
	s.mu.Unlock()
}

func (s *modelAffinityStore) counts(now time.Time) map[string]int {
	result := make(map[string]int)
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, key)
			continue
		}
		result[entry.modelID]++
	}
	return result
}

func orderedRouteCandidates(route *router.Route, affinityModelID string, avoidance *modelAvoidanceStore, scopes []string, now time.Time) []*store.Model {
	if route == nil {
		return nil
	}
	candidates := route.Candidates
	if len(candidates) == 0 && route.Model != nil {
		candidates = []*store.Model{route.Model}
	}
	result := make([]*store.Model, 0, len(candidates))
	if affinityModelID != "" {
		for _, candidate := range candidates {
			if candidate != nil && candidate.ID == affinityModelID && !avoidance.avoided(scopes, candidate.ID, now) {
				result = append(result, candidate)
				break
			}
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID != affinityModelID && !avoidance.avoided(scopes, candidate.ID, now) {
			result = append(result, candidate)
		}
	}
	for _, candidate := range candidates {
		if candidate != nil && avoidance.avoided(scopes, candidate.ID, now) {
			result = append(result, candidate)
		}
	}
	return result
}

type prefixedReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (r *prefixedReadCloser) Read(p []byte) (int, error) { return r.reader.Read(p) }
func (r *prefixedReadCloser) Close() error               { return r.closer.Close() }

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.cancel)
	return err
}

func requestWithFirstByteTimeout(
	parent context.Context,
	timeout time.Duration,
	send func(context.Context) (*http.Response, error),
) (*http.Response, time.Duration, error) {
	if timeout <= 0 {
		resp, err := send(parent)
		return resp, 0, err
	}
	attemptCtx, cancel := context.WithCancel(parent)
	timeoutDone := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		cancel()
		close(timeoutDone)
	})
	startedAt := time.Now()
	resp, err := send(attemptCtx)
	timedOut := !timer.Stop()
	if timedOut {
		<-timeoutDone
	}
	if err != nil {
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if timedOut {
			return nil, 0, errFirstByteTimeout
		}
		return nil, 0, err
	}
	if timedOut {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancel()
		return nil, 0, errFirstByteTimeout
	}
	if resp == nil || resp.Body == nil {
		cancel()
		return resp, 0, nil
	}
	resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}
	if resp.StatusCode >= http.StatusBadRequest {
		return resp, 0, nil
	}
	remaining := timeout - time.Since(startedAt)
	if remaining <= 0 {
		_ = resp.Body.Close()
		return nil, 0, errFirstByteTimeout
	}
	if err := waitForUpstreamFirstByte(resp, remaining); err != nil {
		_ = resp.Body.Close()
		return nil, 0, err
	}
	return resp, time.Since(startedAt), nil
}

func waitForUpstreamFirstByte(resp *http.Response, timeout time.Duration) error {
	if resp == nil || resp.Body == nil || timeout <= 0 {
		return nil
	}
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	go func() {
		buffer := make([]byte, 1)
		n, err := resp.Body.Read(buffer)
		result <- readResult{data: buffer[:n], err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case first := <-result:
		if len(first.data) == 0 {
			_ = resp.Body.Close()
			if first.err == nil {
				return io.ErrNoProgress
			}
			return first.err
		}
		resp.Body = &prefixedReadCloser{reader: io.MultiReader(bytes.NewReader(first.data), resp.Body), closer: resp.Body}
		return nil
	case <-timer.C:
		_ = resp.Body.Close()
		return errFirstByteTimeout
	}
}

func candidateHasBudget(deadline, now time.Time) bool {
	return now.Before(deadline)
}

func candidateFirstByteTimeout(configured time.Duration, deadline, now time.Time) time.Duration {
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return 0
	}
	if configured <= 0 || configured > remaining {
		return remaining
	}
	return configured
}

func observedFirstByteTimeout(configured time.Duration, deadline, now time.Time, degraded bool) time.Duration {
	if degraded || configured <= 0 {
		return candidateFirstByteTimeout(configured, deadline, now)
	}
	observed := configured * 2
	if observed < configured || observed > firstByteGraceLimit {
		observed = firstByteGraceLimit
	}
	if observed < configured {
		observed = configured
	}
	return candidateFirstByteTimeout(observed, deadline, now)
}

func dispatchSelectionReason(route *router.Route, model *store.Model, affinityModelID string, capacityWait time.Duration, affinityDegraded bool) string {
	if route == nil || model == nil {
		return ""
	}
	if affinityModelID != "" && model.ID == affinityModelID {
		if capacityWait >= time.Millisecond {
			return "affinity_capacity_wait"
		}
		return "affinity"
	}
	if affinityModelID != "" && affinityDegraded {
		return "latency_degraded_failover"
	}
	if route.Group != nil {
		if candidateGroup := route.CandidateGroups[model.ID]; candidateGroup != nil && candidateGroup.ID != route.Group.ID {
			return "fallback_group"
		}
	}
	return "priority"
}

type observedUpstreamBody struct {
	io.ReadCloser
	once      sync.Once
	onFailure func(error)
}

func (b *observedUpstreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && b.onFailure != nil {
		b.once.Do(func() { b.onFailure(err) })
	}
	return n, err
}

func shouldFailoverModelStatus(status int) bool {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func modelFailureForStatus(status int) modelFailureClass {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return modelFailureCredential
	case http.StatusTooManyRequests:
		return modelFailureRateLimit
	default:
		return modelFailureUpstream
	}
}

func modelRetryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(header); err == nil {
		if delay := retryAt.Sub(now); delay > 0 {
			return delay
		}
	}
	return modelOpenDuration
}
