// forwarder.go
// 转发逻辑：转换请求 → 发送上游 → 包装流式响应

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"kiro-proxy/upstreamproxy"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

const (
	maxRetries           = 3
	maxTransportRetries  = 1
	transportRetryDelay  = 200 * time.Millisecond
	statusRetryBaseDelay = time.Second
	maxRetryDelay        = 10 * time.Second
	// 网络异常仍只重试一次；快速返回的 502/503/504 最多重试三次。
	retryBudget          = 125 * time.Second
	defaultMaxTokens     = 200000
	affinityCapacityWait = 750 * time.Millisecond
	firstByteGraceLimit  = 60 * time.Second
)

// Forwarder 负责转发请求到上游
type Forwarder struct {
	stats     *store.Stats
	db        *store.DB
	clientMu  sync.RWMutex
	clients   map[string]*http.Client
	health    *modelHealthTracker
	affinity  *modelAffinityStore
	load      *modelLoadTracker
	latency   *modelLatencyTracker
	upstream  *upstreamBucketHealth
	avoidance *modelAvoidanceStore
}

// NewForwarder 创建转发器
func NewForwarder(stats *store.Stats, db *store.DB) *Forwarder {
	client, _ := upstreamproxy.NewClient("", 10*time.Minute)
	clients := make(map[string]*http.Client)
	if client != nil {
		clients[""] = client
	}
	return &Forwarder{
		stats:     stats,
		db:        db,
		clients:   clients,
		health:    newModelHealthTracker(),
		affinity:  newModelAffinityStore(),
		load:      newModelLoadTracker(),
		latency:   newModelLatencyTracker(),
		upstream:  newUpstreamBucketHealth(),
		avoidance: newModelAvoidanceStore(),
	}
}

func (f *Forwarder) clientForProxy(proxyAddr string) (*http.Client, error) {
	proxyAddr = strings.TrimSpace(proxyAddr)

	f.clientMu.RLock()
	if client := f.clients[proxyAddr]; client != nil {
		f.clientMu.RUnlock()
		return client, nil
	}
	f.clientMu.RUnlock()

	f.clientMu.Lock()
	defer f.clientMu.Unlock()
	if client := f.clients[proxyAddr]; client != nil {
		return client, nil
	}

	client, err := upstreamproxy.NewClient(proxyAddr, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	f.clients[proxyAddr] = client
	return client, nil
}

func (f *Forwarder) ModelHealthStatuses() []store.ModelHealthStatus {
	now := time.Now()
	models, modelsErr := f.db.GetModels()
	if modelsErr == nil {
		f.health.syncModels(models)
	}
	statuses := f.health.snapshot(now)
	counts := f.affinity.counts(now)
	active := f.load.snapshot()
	latency := f.latency.snapshot(now)
	avoided := f.avoidance.counts(now)
	limits := make(map[string]int)
	timeouts := make(map[string]int)
	if groups, err := f.db.GetModelGroups(); err == nil {
		for _, group := range groups {
			for _, member := range group.Members {
				timeouts[member.ModelID] = group.FirstByteTimeoutMS
				if member.MaxConcurrency > 0 {
					limits[member.ModelID] = member.MaxConcurrency
				}
			}
		}
	}
	byModel := make(map[string]int, len(statuses))
	for i := range statuses {
		statuses[i].AffinitySessions = counts[statuses[i].ModelConfigID]
		statuses[i].ActiveRequests = active[statuses[i].ModelConfigID]
		statuses[i].CurrentConcurrency = active[statuses[i].ModelConfigID]
		statuses[i].MaxConcurrency = limits[statuses[i].ModelConfigID]
		statuses[i].AvoidedScopes = avoided[statuses[i].ModelConfigID]
		statuses[i].FirstByteTimeoutMS = timeouts[statuses[i].ModelConfigID]
		applyLatencyMetric(&statuses[i], latency[statuses[i].ModelConfigID])
		byModel[statuses[i].ModelConfigID] = i
	}
	allModels := make(map[string]struct{})
	for modelID := range counts {
		allModels[modelID] = struct{}{}
	}
	for modelID := range active {
		allModels[modelID] = struct{}{}
	}
	for modelID := range avoided {
		allModels[modelID] = struct{}{}
	}
	for modelID := range limits {
		allModels[modelID] = struct{}{}
	}
	for modelID := range timeouts {
		allModels[modelID] = struct{}{}
	}
	for modelID := range latency {
		allModels[modelID] = struct{}{}
	}
	for modelID := range allModels {
		if _, exists := byModel[modelID]; exists {
			continue
		}
		status := store.ModelHealthStatus{
			ModelConfigID: modelID, State: "closed", AffinitySessions: counts[modelID],
			ActiveRequests: active[modelID], CurrentConcurrency: active[modelID],
			MaxConcurrency: limits[modelID], AvoidedScopes: avoided[modelID],
			FirstByteTimeoutMS: timeouts[modelID],
		}
		applyLatencyMetric(&status, latency[modelID])
		statuses = append(statuses, status)
	}
	if modelsErr == nil {
		byID := make(map[string]*store.Model, len(models))
		for i := range models {
			byID[models[i].ID] = &models[i]
		}
		for i := range statuses {
			statuses[i].UpstreamBucketState = f.upstream.state(byID[statuses[i].ModelConfigID], now)
		}
	}
	return statuses
}

func applyLatencyMetric(status *store.ModelHealthStatus, metric modelLatencyMetric) {
	if status == nil || metric.sampleCount == 0 {
		return
	}
	status.LastFirstByteMS = metric.lastMS
	status.AverageFirstByteMS = metric.averageMS
	status.EWMAFirstByteMS = metric.ewmaMS
	status.P95FirstByteMS = metric.p95MS
	status.FirstByteSampleCount = metric.sampleCount
	status.SlowFirstByteSamples = metric.slowSampleCount
	status.LatencyDegraded = metric.degraded
	if !metric.degradedUntil.IsZero() {
		status.LatencyDegradedUntil = metric.degradedUntil.Format(time.RFC3339Nano)
	}
}

// Forward 执行完整的转发流程
func (f *Forwarder) Forward(req *http.Request, pre *Preflight, route *router.Route, logger *TrafficLogger) (*http.Response, error) {
	if route == nil || route.Model == nil {
		return nil, fmt.Errorf("无可用模型候选")
	}
	if route.Group == nil {
		model := route.Model
		lease, acquired := f.load.acquire(model.ID, route.CandidateLimits[model.ID])
		if !acquired {
			return nil, fmt.Errorf("模型 %s 已达到并发上限", statsModelID(model))
		}
		bindUsageModel(pre, model)
		resp, contResult, err := f.forwardCandidate(req.Context(), pre, model, logger, true)
		if err != nil {
			lease.release()
			return nil, err
		}
		resp.Body = &leasedUpstreamBody{ReadCloser: resp.Body, lease: lease}
		route.Model = model
		finalized, err := f.finalizeForwardResponse(resp, pre, model, logger, contResult)
		if err != nil {
			_ = resp.Body.Close()
			return nil, err
		}
		return finalized, nil
	}
	return f.forwardModelGroup(req, pre, route, logger)
}

func (f *Forwarder) forwardModelGroup(req *http.Request, pre *Preflight, route *router.Route, logger *TrafficLogger) (*http.Response, error) {
	now := time.Now()
	conversationID := ""
	if pre != nil && pre.Conv != nil {
		conversationID = pre.Conv.ConversationID
	}
	affinityModelID := f.affinity.get(route.Group.ID, conversationID, now)
	scopes := dispatchAvoidanceScopes(route.Group.ID, conversationID, requestClientIP(req))
	candidates := orderedRouteCandidates(route, affinityModelID, f.avoidance, scopes, now)
	candidates = f.latency.order(candidates, now)
	candidates = f.upstream.order(candidates, now)
	deadline := now.Add(retryBudget)
	attempted := 0
	attemptsByGroup := make(map[string]int)
	var lastErr error
	var lastResp *http.Response
	var lastContResult *converter.ResponsesContinuationResult
	var lastModel *store.Model

	for index, model := range candidates {
		if model == nil {
			continue
		}
		if !candidateHasBudget(deadline, time.Now()) {
			break
		}
		candidateGroup := routeGroupForCandidate(route, model)
		if attemptsByGroup[candidateGroup.ID] >= candidateGroup.MaxAttempts {
			continue
		}
		allowed, circuitState := f.health.allow(model, time.Now())
		if !allowed {
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverReason("circuit_" + circuitState)
			}
			logger.WarnWithMeta("dispatcher", fmt.Sprintf("跳过熔断模型: group=%s model=%s state=%s", route.Group.Name, statsModelID(model), circuitState), LogMeta{
				Model: statsModelID(model),
				Metadata: map[string]interface{}{
					"group_id":  route.Group.ID,
					"group":     route.Group.Name,
					"candidate": index,
					"circuit":   circuitState,
				},
			})
			continue
		}
		capacityWait := time.Duration(0)
		if model.ID == affinityModelID {
			capacityWait = affinityCapacityWait
		}
		capacityStarted := time.Now()
		lease, acquired := f.load.acquireWithWait(req.Context(), model.ID, route.CandidateLimits[model.ID], capacityWait)
		capacityWaited := time.Since(capacityStarted)
		if pre != nil && pre.usage != nil && capacityWait > 0 {
			pre.usage.addCapacityWait(capacityWaited)
		}
		if !acquired {
			f.health.neutral(model)
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverReason("capacity_full")
			}
			logger.InfoWithMeta("dispatcher", fmt.Sprintf("跳过并发已满候选: group=%s model=%s", route.Group.Name, statsModelID(model)), LogMeta{
				Model:    statsModelID(model),
				Metadata: map[string]interface{}{"group_id": route.Group.ID, "candidate": index, "max_concurrency": route.CandidateLimits[model.ID]},
			})
			continue
		}
		bucketAllowed, bucketState := f.upstream.allow(model, time.Now())
		if !bucketAllowed {
			lease.release()
			f.health.neutral(model)
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverReason("upstream_bucket_" + bucketState)
			}
			logger.WarnWithMeta("dispatcher", fmt.Sprintf("跳过上游地址异常候选: group=%s model=%s state=%s", route.Group.Name, statsModelID(model), bucketState), LogMeta{
				Model:    statsModelID(model),
				Metadata: map[string]interface{}{"group_id": route.Group.ID, "candidate": index, "upstream_bucket": bucketState},
			})
			continue
		}

		attempted++
		attemptsByGroup[candidateGroup.ID]++
		route.Model = model
		bindUsageModel(pre, model)
		if pre != nil && pre.usage != nil {
			pre.usage.setFailoverCount(attempted - 1)
			pre.usage.setDispatchReason(dispatchSelectionReason(route, model, affinityModelID, capacityWaited, f.latency.isDegraded(affinityModelID, time.Now())))
		}
		logger.InfoWithMeta("dispatcher", fmt.Sprintf("尝试分组候选: group=%s candidate=%d model=%s", route.Group.Name, index, statsModelID(model)), LogMeta{
			Model: statsModelID(model),
			Metadata: map[string]interface{}{
				"group_id":  route.Group.ID,
				"group":     route.Group.Name,
				"candidate": index,
			},
		})

		var contResult *converter.ResponsesContinuationResult
		var attemptResp *http.Response
		configuredFirstByteTimeout := time.Duration(candidateGroup.FirstByteTimeoutMS) * time.Millisecond
		firstByteTimeout := observedFirstByteTimeout(
			configuredFirstByteTimeout,
			deadline,
			time.Now(),
			f.latency.isDegraded(model.ID, time.Now()),
		)
		resp, firstByteDuration, err := requestWithFirstByteTimeout(req.Context(), firstByteTimeout, func(attemptCtx context.Context) (*http.Response, error) {
			var forwardErr error
			attemptResp, contResult, forwardErr = f.forwardCandidate(attemptCtx, pre, model, logger, false)
			return attemptResp, forwardErr
		})
		if err != nil {
			lease.release()
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			lastErr = err
			failureClass := "candidate_error"
			if errors.Is(err, errFirstByteTimeout) {
				failureClass = "first_byte_slow"
				f.latency.record(model.ID, firstByteTimeout, configuredFirstByteTimeout, time.Now())
				f.health.neutral(model)
				f.upstream.neutral(model, time.Now())
				if f.latency.isDegraded(model.ID, time.Now()) {
					f.affinity.clear(route.Group.ID, conversationID, model.ID)
				}
			} else if isUpstreamTransportFailure(err) {
				failureClass = string(modelFailureTransport)
				f.health.failure(model, modelFailureTransport, 0, time.Now())
				f.upstream.recordFailure(model, time.Now())
				f.affinity.clear(route.Group.ID, conversationID, model.ID)
				markAvoidanceScopes(f.avoidance, scopes, model.ID, time.Now())
			} else {
				f.health.neutral(model)
				f.upstream.neutral(model, time.Now())
			}
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverReason(failureClass)
			}
			logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选网络失败: group=%s model=%s error=%v", route.Group.Name, statsModelID(model), err), LogMeta{
				Model: statsModelID(model),
				Metadata: map[string]interface{}{
					"group_id":      route.Group.ID,
					"failure_class": failureClass,
					"candidate":     index,
				},
			})
			continue
		}
		resp.Body = &leasedUpstreamBody{ReadCloser: resp.Body, lease: lease}
		if firstByteDuration > 0 {
			f.latency.record(model.ID, firstByteDuration, configuredFirstByteTimeout, time.Now())
		}
		f.upstream.recordSuccess(model, time.Now())

		if shouldFailoverModelStatus(resp.StatusCode) {
			bodyDetail := preserveResponseBody(resp)
			failureClass := modelFailureForStatus(resp.StatusCode)
			retryAfter := time.Duration(0)
			if failureClass == modelFailureRateLimit {
				retryAfter = modelRetryAfter(resp.Header.Get("Retry-After"), time.Now())
			}
			f.health.failure(model, failureClass, retryAfter, time.Now())
			f.affinity.clear(route.Group.ID, conversationID, model.ID)
			markAvoidanceScopes(f.avoidance, scopes, model.ID, time.Now())
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			if lastResp != nil {
				lastResp.Body.Close()
			}
			lastResp = resp
			lastContResult = contResult
			lastModel = model
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverReason(fmt.Sprintf("http_%d", resp.StatusCode))
			}
			logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选返回可切换错误: group=%s model=%s status=%d", route.Group.Name, statsModelID(model), resp.StatusCode), LogMeta{
				Model:  statsModelID(model),
				Status: resp.StatusCode,
				Metadata: map[string]interface{}{
					"group_id":      route.Group.ID,
					"failure_class": string(failureClass),
					"candidate":     index,
				},
			}, bodyDetail)
			continue
		}
		if resp.StatusCode < http.StatusBadRequest && pre != nil && pre.usage != nil {
			pre.usage.markFirstByte(time.Now())
		}

		if lastResp != nil {
			lastResp.Body.Close()
		}
		if resp.StatusCode < http.StatusBadRequest {
			f.health.success(model)
			f.affinity.set(route.Group.ID, conversationID, model.ID, time.Now())
			clearAvoidanceScopes(f.avoidance, scopes, model.ID)
			resp.Body = &observedUpstreamBody{
				ReadCloser: resp.Body,
				onFailure: func(readErr error) {
					f.health.failure(model, modelFailureTransport, 0, time.Now())
					f.upstream.recordFailure(model, time.Now())
					f.affinity.clear(route.Group.ID, conversationID, model.ID)
					logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选响应流中断: group=%s model=%s error=%v", route.Group.Name, statsModelID(model), readErr), LogMeta{
						Model: statsModelID(model),
						Metadata: map[string]interface{}{
							"group_id":      route.Group.ID,
							"failure_class": string(modelFailureTransport),
						},
					})
				},
			}
		} else {
			f.health.neutral(model)
		}
		return f.finalizeForwardResponse(resp, pre, model, logger, contResult)
	}

	if lastResp != nil && lastModel != nil {
		route.Model = lastModel
		bindUsageModel(pre, lastModel)
		return f.finalizeForwardResponse(lastResp, pre, lastModel, logger, lastContResult)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("模型组 %s 请求失败: %w", route.Group.Name, lastErr)
	}
	if attempted == 0 {
		return nil, fmt.Errorf("模型组 %s 当前没有可用候选", route.Group.Name)
	}
	return nil, fmt.Errorf("模型组 %s 已超过请求时间预算", route.Group.Name)
}

func requestClientIP(req *http.Request) string {
	if req == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(req.RemoteAddr))
	if err == nil {
		return host
	}
	return strings.TrimSpace(req.RemoteAddr)
}

func routeGroupForCandidate(route *router.Route, model *store.Model) *store.ModelGroup {
	if route == nil {
		return nil
	}
	if model != nil && route.CandidateGroups != nil {
		if group := route.CandidateGroups[model.ID]; group != nil {
			return group
		}
	}
	return route.Group
}

func dispatchAvoidanceScopes(groupID, conversationID, clientIP string) []string {
	scopes := make([]string, 0, 2)
	if clientIP != "" {
		scopes = append(scopes, "ip:"+groupID+":"+clientIP)
	}
	if conversationID != "" {
		scopes = append(scopes, "turn:"+groupID+":"+conversationID)
	}
	return scopes
}

func markAvoidanceScopes(store *modelAvoidanceStore, scopes []string, modelID string, now time.Time) {
	for _, scope := range scopes {
		store.mark(scope, modelID, now)
	}
}

func clearAvoidanceScopes(store *modelAvoidanceStore, scopes []string, modelID string) {
	for _, scope := range scopes {
		store.clear(scope, modelID)
	}
}

func (f *Forwarder) forwardCandidate(ctx context.Context, pre *Preflight, model *store.Model, logger *TrafficLogger, retry bool) (*http.Response, *converter.ResponsesContinuationResult, error) {
	logger.Info("converter", fmt.Sprintf("开始转换: provider=%s, model_id=%s", model.Provider, model.ModelID))
	conversionStarted := time.Now()
	upstreamBody, upstreamURL, err := convertRequest(pre, model)
	if pre != nil && pre.usage != nil {
		pre.usage.setConversionDuration(conversionStarted)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("转换失败: %w", err)
	}

	var contResult *converter.ResponsesContinuationResult
	continuationStateKey := ""
	if pre != nil {
		continuationStateKey = responsesContinuationStateKeyFor(pre, model, resolveReasoningEffort(pre, model))
	}
	if pre != nil && pre.Conv != nil && model.EnableContinuation && useResponsesAPI(pre, model) && !pre.Conv.IsClassifier && continuationStateKey != "" {
		prevSession, sessionErr := f.db.GetResponsesSession(continuationStateKey)
		if sessionErr != nil {
			return nil, nil, fmt.Errorf("读取 Responses 续传状态失败: %w", sessionErr)
		}
		var prevState *converter.ResponsesContinuationState
		if prevSession != nil && prevSession.ResponseID != "" {
			prevState = &converter.ResponsesContinuationState{ResponseID: prevSession.ResponseID, InputJSON: prevSession.InputJSON, ItemCount: prevSession.ItemCount}
		}
		contResult, err = converter.ApplyResponsesContinuation(upstreamBody, prevState)
		if err == nil && contResult != nil {
			if contResult.UsedContinuation {
				prevID := prevState.ResponseID
				if len(prevID) > 12 {
					prevID = prevID[:12]
				}
				logger.Info("forwarder", fmt.Sprintf("续传命中: convId=%s, prev_response=%s, delta_size=%d bytes", pre.Conv.ConversationID, prevID, len(contResult.Body)))
			}
			upstreamBody = contResult.Body
		}
	}

	logPrefixDrift(logger, pre, model, upstreamBody, contResult)
	logger.InfoBytes("forwarder", fmt.Sprintf("转发: %s → %s (body=%d bytes)", model.ModelID, upstreamURL, len(upstreamBody)), upstreamBody)
	sessionID := ""
	if pre != nil && pre.Conv != nil {
		if pre.Conv.IsClassifier {
			sessionID = "kiro-classifier"
		} else {
			sessionID = pre.Conv.ConversationID
		}
	}
	resp, err := f.sendCandidateRequest(ctx, upstreamURL, upstreamBody, model, logger, sessionID, pre.usage, retry)
	if err != nil {
		return nil, contResult, err
	}

	if contResult != nil && contResult.UsedContinuation && resp.StatusCode >= 400 && resp.StatusCode < 500 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyStr := string(body)
		if shouldRetryWithoutContinuation(bodyStr) {
			logger.Warn("forwarder", fmt.Sprintf("续传请求被上游拒绝，清理状态并回退全量: %s", truncate(bodyStr, 200)))
			if deleteErr := f.db.DeleteResponsesSession(continuationStateKey); deleteErr != nil {
				logger.Error("forwarder", fmt.Sprintf("清理 Responses 续传状态失败: %v", deleteErr))
			}
			upstreamBody = contResult.FallbackBody
			contResult.UsedContinuation = false
			resp, err = f.sendCandidateRequest(ctx, upstreamURL, upstreamBody, model, logger, sessionID, pre.usage, retry)
			if err != nil {
				return nil, contResult, err
			}
		} else {
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return resp, contResult, nil
}

func (f *Forwarder) sendCandidateRequest(ctx context.Context, url string, body []byte, model *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder, retry bool) (*http.Response, error) {
	if retry {
		return f.sendWithRetry(ctx, url, body, model, logger, sessionID, usage)
	}
	return f.doRequest(ctx, url, body, model, sessionID, usage)
}

func (f *Forwarder) finalizeForwardResponse(resp *http.Response, pre *Preflight, model *store.Model, logger *TrafficLogger, contResult *converter.ResponsesContinuationResult) (*http.Response, error) {
	if pre != nil && pre.usage != nil {
		pre.usage.wrapResponse(resp)
	}
	if pre == nil || pre.Protocol != "kiro" {
		return resp, nil
	}
	return f.wrapKiroStream(resp, pre, model, logger, contResult)
}

func bindUsageModel(pre *Preflight, model *store.Model) {
	if pre == nil || pre.usage == nil {
		return
	}
	pre.usage.setModel(model)
	pre.usage.setReasoningEffort(resolveReasoningEffort(pre, model))
}

func preserveResponseBody(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return truncate(string(body), 500)
}

// sendWithRetry 发送请求，失败重试
func (f *Forwarder) sendWithRetry(ctx context.Context, url string, body []byte, m *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder) (*http.Response, error) {
	lastErr := context.DeadlineExceeded
	deadline := time.Now().Add(retryBudget)
	transportRetries := 0
	statusRetries := 0
	nextRetryDelay := time.Duration(0)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if attempt > 0 {
			delay := nextRetryDelay
			logger.Warn("forwarder", fmt.Sprintf("重试 %d/%d，等待 %v", attempt, maxRetries, delay))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			remaining = time.Until(deadline)
			if remaining <= 0 {
				break
			}
		}

		attemptCtx, cancelAttempt := context.WithDeadline(ctx, deadline)
		resp, err := f.doRequest(attemptCtx, url, body, m, sessionID, usage)
		if err != nil {
			cancelAttempt()
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logger.Error("forwarder", fmt.Sprintf("请求失败 (attempt %d): %v", attempt, err))
			transportRetries++
			if transportRetries > maxTransportRetries {
				return nil, fmt.Errorf("上游失败（网络重试 %d 次）: %w", maxTransportRetries, lastErr)
			}
			nextRetryDelay = transportRetryDelay
			continue
		}
		if resp.Body != nil {
			resp.Body = &cancelReadCloser{ReadCloser: resp.Body, cancel: cancelAttempt}
		} else {
			cancelAttempt()
		}

		if shouldRetryUpstreamStatus(resp.StatusCode) && attempt < maxRetries {
			statusRetries++
			nextRetryDelay = upstreamStatusRetryDelay(statusRetries, resp.Header.Get("Retry-After"))
			if resp.ContentLength >= 0 && resp.ContentLength <= 32*1024 {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			logger.Warn("forwarder", fmt.Sprintf("上游返回 %d（第 %d/%d 次请求），准备重试", resp.StatusCode, attempt+1, maxRetries+1))
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("上游失败（重试 %d 次）: %w", maxRetries, lastErr)
}

func shouldRetryUpstreamStatus(status int) bool {
	return status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func upstreamStatusRetryDelay(retryNumber int, retryAfter string) time.Duration {
	retryAfter = strings.TrimSpace(retryAfter)
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		return minRetryDelay(time.Duration(seconds) * time.Second)
	}
	if retryAt, err := http.ParseTime(retryAfter); err == nil {
		return minRetryDelay(time.Until(retryAt))
	}

	if retryNumber < 1 {
		retryNumber = 1
	}
	delay := statusRetryBaseDelay * time.Duration(1<<(retryNumber-1))
	return minRetryDelay(delay)
}

func minRetryDelay(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func (f *Forwarder) doRequest(ctx context.Context, url string, body []byte, m *store.Model, sessionID string, usage *requestUsageRecorder) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", ua.CodexUserAgent())

	switch m.Provider {
	case "anthropic":
		req.Header.Set("x-api-key", m.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "prompt-caching-2024-07-31")
	case "gemini":
		// Gemini 使用 API key 作为 query parameter
		if m.APIKey != "" {
			if req.URL.RawQuery != "" {
				req.URL.RawQuery += "&key=" + m.APIKey
			} else {
				req.URL.RawQuery = "key=" + m.APIKey
			}
		}
	case "codex":
		beforeToken := m.APIKey
		beforeRefresh := m.RefreshToken
		authStarted := time.Now()
		err := codex.EnsureAccessToken(ctx, m)
		if usage != nil {
			usage.addAuthDuration(time.Since(authStarted))
		}
		if err != nil {
			return nil, err
		}
		if (m.APIKey != beforeToken || m.RefreshToken != beforeRefresh) && f.db != nil {
			_ = f.db.UpdateCodexTokenAndUsage(m)
		}
		codex.ApplyHeaders(req, m.APIKey, m.CodexAccountID, true)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
		req.Header.Set("x-openai-internal-codex-residency", "us")
		req.Header.Set("x-client-request-id", sessionIDOrRandom(sessionID))
		req.Header.Set("x-codex-installation-id", "kiro-proxy")
	default:
		req.Header.Set("Authorization", "Bearer "+m.APIKey)
	}

	// 中转站缓存关联（多种 header 兼容）。sub2api 明确读取 session_id，
	// Nginx 反代需开启 underscores_in_headers；同时保留短横线写法兼容其他中转。
	if sessionID != "" {
		req.Header.Set("session_id", sessionID)
		req.Header.Set("conversation_id", sessionID)
		req.Header.Set("x-session-id", sessionID)
		req.Header.Set("session-id", sessionID)
	}

	client, err := f.clientForProxy(m.ProxyURL)
	if err != nil {
		return nil, err
	}
	if usage != nil {
		usage.beginAttempt()
		req = req.WithContext(usage.traceContext(ctx))
	}
	return client.Do(req)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func responsesContinuationStateKey(conv *converter.ParsedConversation) string {
	if conv == nil {
		return ""
	}
	if conv.ContinuationStateKey != "" {
		return conv.ContinuationStateKey
	}
	return conv.ConversationID
}

func responsesContinuationStateKeyFor(pre *Preflight, model *store.Model, reasoningEffort string) string {
	base := ""
	if pre != nil {
		base = responsesContinuationStateKey(pre.Conv)
	}
	if base == "" {
		return ""
	}

	modelKey := ""
	if model != nil {
		modelKey = strings.TrimSpace(model.ID)
		if modelKey == "" {
			modelKey = strings.TrimSpace(model.ModelID)
		}
	}
	return strings.Join([]string{base, modelKey, strings.TrimSpace(reasoningEffort)}, "|")
}

func isPreviousResponseRejected(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "previous_response") && !strings.Contains(lower, "previous response") {
		return false
	}
	markers := []string{
		"not found",
		"not_found",
		"invalid",
		"unsupported",
		"unknown parameter",
		"unrecognized",
		"does not exist",
		"expired",
		"cannot be used",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func shouldRetryWithoutContinuation(body string) bool {
	if isPreviousResponseRejected(body) {
		return true
	}

	lower := strings.ToLower(body)
	if !strings.Contains(lower, "invalid_request_error") {
		return false
	}
	markers := []string{
		"input[",
		"function_call_output",
		"call_id",
		".output",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func logPrefixDrift(logger *TrafficLogger, pre *Preflight, m *store.Model, body []byte, contResult *converter.ResponsesContinuationResult) {
	if logger == nil || pre == nil || pre.Conv == nil || pre.Conv.IsClassifier {
		return
	}

	staticSystem, dynamicSystem := converter.OptimizeSystemForCacheForAudit(pre.Conv.SystemText)
	currentQuestion, envContext := converter.OptimizeCurrentContentForCacheForAudit(pre.Conv.CurrentContent)
	inputHash := ""
	inputItems := 0
	if contResult != nil && contResult.FullInputJSON != "" {
		inputHash = converter.HashInput(contResult.FullInputJSON)
		inputItems = contResult.InputItemCount
	}

	api := "chat"
	if useResponsesAPI(pre, m) {
		api = "responses"
	}
	openAICache := m.Provider != "anthropic" && m.Provider != "gemini"
	providerToolsHash := ""
	toolReport := converter.ToolConversionReport{}
	toolResultReport := converter.ToolResultConversionReportForAudit(pre.Conv)
	if openAICache {
		providerToolsHash = converter.ProviderToolsHashForAudit(pre.Conv.RawTools)
		toolReport = converter.ToolConversionReportForAudit(pre.Conv.RawTools, true)
	}
	actualCacheKey := ""
	expectedCacheKey := ""
	providerInputHash := inputHash
	providerInputItems := inputItems
	previousResponseID := ""
	var auditBody struct {
		PromptCacheKey     string          `json:"prompt_cache_key"`
		Input              json.RawMessage `json:"input"`
		Messages           json.RawMessage `json:"messages"`
		Tools              json.RawMessage `json:"tools"`
		PreviousResponseID string          `json:"previous_response_id"`
	}
	if openAICache && json.Unmarshal(body, &auditBody) == nil {
		actualCacheKey = auditBody.PromptCacheKey
		previousResponseID = auditBody.PreviousResponseID
		if len(auditBody.Input) > 0 {
			api = "responses"
		}
		if providerInputHash == "" {
			prefix := auditBody.Input
			if len(prefix) == 0 {
				prefix = auditBody.Messages
			}
			if len(prefix) > 0 {
				providerInputHash = converter.HashInput(string(prefix))
				var items []json.RawMessage
				if json.Unmarshal(prefix, &items) == nil {
					providerInputItems = len(items)
				}
			}
		}
	}
	if openAICache {
		expectedCacheKey = converter.PromptCacheKeyForAuditWithProviderToolsHash(
			pre.Conv,
			m.ModelID,
			api,
			providerToolsHash,
		)
	}

	logger.Info("cache", fmt.Sprintf(
		"prefix_audit: model=%s endpoint=%s api=%s conv=%s body=%d system_static=%d system_dynamic=%d env=%d question=%d history=%d tools=%d raw_tools_hash=%s provider_tools_hash=%s tool_invalid=%d tool_schema_invalid=%d tool_websearch_filtered=%d tool_desc_truncated=%d tool_over_limit=%d tool_results=%d tool_blocks=%d tool_nontext_blocks=%d cache_key=%s expected_key=%s provider_input_items=%d provider_input_hash=%s previous_response=%v continuation=%v",
		statsModelID(m),
		m.Endpoint,
		api,
		pre.Conv.ConversationID,
		len(body),
		len(staticSystem),
		len(dynamicSystem),
		len(envContext),
		len(currentQuestion),
		len(pre.Conv.History),
		len(pre.Conv.RawTools),
		pre.Conv.ToolsHash,
		providerToolsHash,
		toolReport.InvalidTools,
		toolReport.InvalidSchemas,
		toolReport.FilteredWebSearchTools,
		toolReport.DescriptionTruncations,
		toolReport.OverLimitTools,
		toolResultReport.Results,
		toolResultReport.Blocks,
		toolResultReport.NonTextBlocks,
		actualCacheKey,
		expectedCacheKey,
		providerInputItems,
		providerInputHash,
		previousResponseID != "",
		contResult != nil && contResult.UsedContinuation,
	))
}

func sanitizedHeaders(headers http.Header) map[string][]string {
	result := make(map[string][]string, len(headers))
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		values := append([]string(nil), headers.Values(k)...)
		if isSensitiveHeader(k) {
			for i := range values {
				values[i] = "<redacted>"
			}
		}
		result[k] = values
	}
	return result
}

func isSensitiveHeader(key string) bool {
	key = strings.ToLower(key)
	return key == "authorization" || key == "proxy-authorization" || key == "x-api-key" || key == "api-key" || key == "cookie" || key == "set-cookie"
}

func reqFromResponse(resp *http.Response) *http.Request {
	if resp == nil {
		return nil
	}
	return resp.Request
}

func kiroModelID(pre *Preflight, m *store.Model) string {
	if pre != nil && pre.Conv != nil && pre.Conv.ModelID != "" {
		return pre.Conv.ModelID
	}
	if m != nil {
		if m.Name != "" {
			return m.Name
		}
		return m.ModelID
	}
	return ""
}

func buildUpstreamErrorMessage(statusCode int, status string, body []byte) string {
	reason := extractUpstreamErrorReason(body)
	if reason == "" {
		reason = strings.TrimSpace(status)
	}
	if reason == "" {
		reason = "上游服务没有返回具体原因"
	}

	return fmt.Sprintf("上游模型服务返回错误，HTTP 状态码：%d。\n\n错误原因：%s\n\n请检查当前模型的 API Key、额度、接口地址、模型名称和请求参数是否正确；如果这是 5xx 错误，通常表示上游服务临时异常，稍后重试即可。", statusCode, reason)
}

func extractUpstreamErrorReason(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}

	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if reason := findErrorReason(parsed, 0); reason != "" {
			return truncate(reason, 800)
		}
	}
	return truncate(text, 800)
}

func findErrorReason(v interface{}, depth int) string {
	if depth > 4 || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]interface{}:
		for _, key := range []string{"message", "reason", "detail", "error", "type", "code"} {
			if val, ok := x[key]; ok {
				if reason := findErrorReason(val, depth+1); reason != "" {
					return reason
				}
			}
		}
	case []interface{}:
		for _, item := range x {
			if reason := findErrorReason(item, depth+1); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func sessionIDOrRandom(sessionID string) string {
	if strings.TrimSpace(sessionID) != "" {
		return sessionID
	}
	return fmt.Sprintf("kiro-proxy-%d", time.Now().UnixNano())
}

func streamLogMeta(pre *Preflight, m *store.Model, resp *http.Response, in, out, cacheRead, cacheWrite int, credits float64) LogMeta {
	meta := LogMeta{
		Model:            statsModelID(m),
		InputTokens:      int64(in),
		OutputTokens:     int64(out),
		CacheReadTokens:  int64(cacheRead),
		CacheWriteTokens: int64(cacheWrite),
		Credits:          credits,
		Metadata:         map[string]interface{}{},
	}
	if resp != nil {
		meta.Status = resp.StatusCode
	}
	if pre != nil {
		meta.LatencyMS = time.Since(pre.StartTime).Milliseconds()
		meta.Metadata["protocol"] = pre.Protocol
		meta.Metadata["classifier"] = pre.IsClassifier
		meta.Metadata["conversation_id"] = pre.ConversationID
	}
	if m != nil {
		meta.Metadata["provider"] = m.Provider
		meta.Metadata["endpoint"] = m.Endpoint
	}
	return meta
}
