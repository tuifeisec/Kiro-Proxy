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
	minimumRetryBudget          = 125 * time.Second
	defaultMaxTokens            = 200000
	affinityCapacityWait        = 750 * time.Millisecond
	firstByteGraceLimit         = 5 * time.Minute
	singleModelFirstByteTimeout = 5 * time.Minute
	upstreamReadIdleTimeout     = 5 * time.Minute
	upstreamErrorBodyTimeout    = 10 * time.Second
	upstreamErrorBodyLimit      = 1 << 20
)

var (
	errUpstreamErrorBodyTimeout  = errors.New("upstream error response body timeout")
	errUpstreamErrorBodyTooLarge = errors.New("upstream error response body too large")
)

// Forwarder 负责转发请求到上游
type Forwarder struct {
	stats       *store.Stats
	db          *store.DB
	clientMu    sync.RWMutex
	clients     map[string]*http.Client
	health      *modelHealthTracker
	affinity    *modelAffinityStore
	load        *modelLoadTracker
	latency     *modelLatencyTracker
	upstream    *upstreamBucketHealth
	avoidance   *modelAvoidanceStore
	imageCaps   *modelImageCapabilityStore
	prefixAudit *prefixAuditTracker
}

// NewForwarder 创建转发器
func NewForwarder(stats *store.Stats, db *store.DB) *Forwarder {
	client, _ := upstreamproxy.NewClient("", 0)
	clients := make(map[string]*http.Client)
	if client != nil {
		clients[""] = client
	}
	return &Forwarder{
		stats:       stats,
		db:          db,
		clients:     clients,
		health:      newModelHealthTracker(),
		affinity:    newModelAffinityStore(),
		load:        newModelLoadTracker(),
		latency:     newModelLatencyTracker(),
		upstream:    newUpstreamBucketHealth(),
		avoidance:   newModelAvoidanceStore(),
		imageCaps:   newModelImageCapabilityStore(),
		prefixAudit: newPrefixAuditTracker(),
	}
}

func requestWithProtocolFallback(
	parent context.Context,
	timeout time.Duration,
	send func(context.Context) (*http.Response, error),
	probe responsePrecommitProbe,
) (*http.Response, time.Duration, error) {
	startedAt := time.Now()
	resp, duration, err := requestWithFirstByteTimeout(parent, timeout, send, probe)
	if err == nil || !upstreamproxy.IsHTTP2Failure(err) || parent.Err() != nil {
		return resp, duration, err
	}

	retryTimeout := timeout
	if timeout > 0 {
		retryTimeout -= time.Since(startedAt)
		if retryTimeout <= 0 {
			return nil, 0, err
		}
	}
	resp, _, retryErr := requestWithFirstByteTimeout(parent, retryTimeout, send, probe)
	if retryErr != nil {
		return nil, 0, retryErr
	}
	return resp, time.Since(startedAt), nil
}

func modelGroupRetryBudget(route *router.Route) time.Duration {
	if route == nil || route.Group == nil {
		return minimumRetryBudget
	}
	stages := route.Stages
	if len(stages) == 0 {
		stages = []router.RouteStage{{
			GroupID:    route.Group.ID,
			Candidates: route.Candidates,
		}}
	}

	budget := time.Duration(0)
	for _, stage := range stages {
		group := route.Group
		if stage.GroupID != "" && stage.GroupID != route.Group.ID {
			for _, candidateGroup := range route.CandidateGroups {
				if candidateGroup != nil && candidateGroup.ID == stage.GroupID {
					group = candidateGroup
					break
				}
			}
		}
		attempts := group.MaxAttempts
		if attempts <= 0 {
			attempts = 1
		}
		if len(stage.Candidates) > 0 && attempts > len(stage.Candidates) {
			attempts = len(stage.Candidates)
		}
		configured := time.Duration(group.FirstByteTimeoutMS) * time.Millisecond
		if configured <= 0 {
			configured = 10 * time.Second
		}
		perAttempt := configured * 2
		if perAttempt < configured || perAttempt > firstByteGraceLimit {
			perAttempt = firstByteGraceLimit
		}
		budget += perAttempt * time.Duration(attempts)
	}
	if budget < minimumRetryBudget {
		return minimumRetryBudget
	}
	return budget
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

	client, err := upstreamproxy.NewClient(proxyAddr, 0)
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
		imageState := conversationImageState{}
		if pre != nil {
			imageState = analyzeConversationImages(pre.Conv)
		}
		imageCapability, capabilitySource := f.imageCaps.effective(model, time.Now())
		candidateView := candidateConversationForModel(pre, imageState, imageCapability)
		hasImage := candidateView.imageCount > 0
		if hasImage && imageCapability == modelImageUnsupported {
			logger.InfoWithMeta("dispatcher", fmt.Sprintf("跳过不支持图片的模型: model=%s", statsModelID(model)), LogMeta{
				Model: statsModelID(model),
				Metadata: map[string]interface{}{
					"has_image":                       true,
					"image_count":                     candidateView.imageCount,
					"image_bytes":                     candidateView.imageBytes,
					"current_image_count":             imageState.currentCount,
					"pending_history_image_count":     imageState.pendingHistoryCount,
					"projectable_history_image_count": imageState.projectableHistoryCount,
					"history_view":                    candidateView.historyView,
					"capability_source":               capabilitySource,
				},
			})
			return nil, fmt.Errorf("模型 %s 不支持图片输入", statsModelID(model))
		}
		if candidateView.projectedImageCount > 0 {
			logger.InfoWithMeta("dispatcher", fmt.Sprintf("文本模型使用历史图片投影视图: model=%s", statsModelID(model)), LogMeta{
				Model: statsModelID(model),
				Metadata: map[string]interface{}{
					"history_view":                    candidateView.historyView,
					"projected_image_count":           candidateView.projectedImageCount,
					"projected_image_bytes":           candidateView.projectedImageBytes,
					"conversation_image_count":        imageState.totalCount(),
					"projectable_history_image_count": imageState.projectableHistoryCount,
				},
			})
		}
		lease, acquired := f.load.acquire(model.ID, route.CandidateLimits[model.ID])
		if !acquired {
			return nil, fmt.Errorf("模型 %s 已达到并发上限", statsModelID(model))
		}
		bindUsageModel(pre, model)
		var contResult *converter.ResponsesContinuationResult
		resp, _, err := requestWithProtocolFallback(req.Context(), singleModelFirstByteTimeout, func(attemptCtx context.Context) (*http.Response, error) {
			var forwardErr error
			var attemptResp *http.Response
			attemptResp, contResult, forwardErr = f.forwardCandidate(attemptCtx, candidateView.pre, model, logger, singleModelFirstByteTimeout)
			return attemptResp, forwardErr
		}, func(attemptCtx context.Context, attemptResp *http.Response, remaining time.Duration) error {
			if !isSSEResponse(attemptResp) {
				return waitForUpstreamFirstByte(attemptResp, remaining)
			}
			return probeSSEPrecommit(attemptCtx, attemptResp, model.Provider, model.Endpoint, remaining, 0)
		})
		if err != nil {
			lease.release()
			return nil, err
		}
		if hasImage && resp.StatusCode == http.StatusBadRequest {
			bodyDetail := preserveResponseBody(resp)
			if isModelCapabilityMismatch(resp.StatusCode, bodyDetail) {
				f.imageCaps.observe(model, modelImageUnsupported, time.Now())
			}
		} else if hasImage && resp.StatusCode < http.StatusBadRequest {
			f.imageCaps.observe(model, modelImageSupported, time.Now())
		}
		resp.Body = &leasedUpstreamBody{ReadCloser: resp.Body, lease: lease}
		route.Model = model
		finalized, err := f.finalizeForwardResponse(req.Context(), resp, pre, model, logger, contResult, nil)
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
	imageState := conversationImageState{}
	conversationID := ""
	if pre != nil && pre.Conv != nil {
		conversationID = pre.Conv.ConversationID
		imageState = analyzeConversationImages(pre.Conv)
	}
	imageCount := imageState.totalCount()
	imageBytes := imageState.totalBytes()
	requiresVision := imageState.requiresVision()
	affinityModelID := f.affinity.get(route.Group.ID, conversationID, now)
	scopes := dispatchAvoidanceScopes(route.Group.ID, conversationID, requestClientIP(req))

	// 使用阶段信息，而不是摊平后的全局候选数组
	stages := route.Stages
	if len(stages) == 0 {
		// 向后兼容：如果没有阶段信息，构造单阶段
		stages = []router.RouteStage{{
			GroupID:    route.Group.ID,
			GroupName:  route.Group.Name,
			Candidates: route.Candidates,
		}}
	}

	deadline := now.Add(modelGroupRetryBudget(route))
	attempted := 0
	attemptsByGroup := make(map[string]int)
	var lastErr error
	var lastResp *http.Response
	var lastContResult *converter.ResponsesContinuationResult
	var lastModel *store.Model

	// 按阶段遍历，每个阶段内应用调度策略
	for stageIndex, stage := range stages {
		if !candidateHasBudget(deadline, time.Now()) {
			break
		}

		candidateGroup := stage.GroupID
		groupMaxAttempts := route.Group.MaxAttempts
		if candidateGroup != route.Group.ID {
			// 这是 fallback 阶段，查找对应分组配置
			for _, g := range route.CandidateGroups {
				if g != nil && g.ID == candidateGroup {
					groupMaxAttempts = g.MaxAttempts
					break
				}
			}
		}

		if attemptsByGroup[candidateGroup] >= groupMaxAttempts {
			logger.Info("dispatcher", fmt.Sprintf("跳过已达最大尝试次数的阶段: stage=%d group=%s attempts=%d", stageIndex, stage.GroupName, attemptsByGroup[candidateGroup]))
			continue
		}

		// 阶段内应用调度策略（亲和、延迟、上游健康、图片能力）
		stageCandidates := orderedRouteCandidates(&router.Route{
			Model:           stage.Candidates[0],
			Candidates:      stage.Candidates,
			CandidateLimits: route.CandidateLimits,
			CandidateGroups: route.CandidateGroups,
			Group:           route.Group,
		}, affinityModelID, f.avoidance, scopes, now)
		stageCandidates = f.latency.order(stageCandidates, now)
		stageCandidates = f.upstream.order(stageCandidates, now)
		var skippedImageCandidates []skippedImageCandidate
		if requiresVision {
			stageCandidates, skippedImageCandidates = imageCompatibleCandidates(stageCandidates, true, f.imageCaps, now)
		}

		for _, skipped := range skippedImageCandidates {
			logger.InfoWithMeta("dispatcher", fmt.Sprintf("跳过不支持图片的候选: stage=%d group=%s model=%s", stageIndex, stage.GroupName, statsModelID(skipped.model)), LogMeta{
				Model: statsModelID(skipped.model),
				Metadata: map[string]interface{}{
					"stage":                       stageIndex,
					"stage_group":                 stage.GroupName,
					"has_image":                   true,
					"image_count":                 imageCount,
					"image_bytes":                 imageBytes,
					"current_image_count":         imageState.currentCount,
					"pending_history_image_count": imageState.pendingHistoryCount,
					"capability_source":           skipped.source,
				},
			})
		}

		if requiresVision && len(stageCandidates) == 0 {
			logger.Warn("dispatcher", fmt.Sprintf("阶段 %d (%s) 没有支持图片的可用候选，进入下一阶段", stageIndex, stage.GroupName))
			continue
		}

		// 在阶段内尝试候选
		stageSuccess := false
		for candidateIndex, model := range stageCandidates {
			if model == nil {
				continue
			}
			if !candidateHasBudget(deadline, time.Now()) {
				break
			}

			if attemptsByGroup[candidateGroup] >= groupMaxAttempts {
				break
			}

			allowed, circuitState := f.health.allow(model, time.Now())
			if !allowed {
				if pre != nil && pre.usage != nil {
					pre.usage.setFailoverReason("circuit_" + circuitState)
				}
				logger.WarnWithMeta("dispatcher", fmt.Sprintf("跳过熔断模型: stage=%d group=%s model=%s state=%s", stageIndex, stage.GroupName, statsModelID(model), circuitState), LogMeta{
					Model: statsModelID(model),
					Metadata: map[string]interface{}{
						"stage":       stageIndex,
						"stage_group": stage.GroupName,
						"candidate":   candidateIndex,
						"circuit":     circuitState,
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
				logger.InfoWithMeta("dispatcher", fmt.Sprintf("跳过并发已满候选: stage=%d group=%s model=%s", stageIndex, stage.GroupName, statsModelID(model)), LogMeta{
					Model:    statsModelID(model),
					Metadata: map[string]interface{}{"stage": stageIndex, "stage_group": stage.GroupName, "candidate": candidateIndex, "max_concurrency": route.CandidateLimits[model.ID]},
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
				logger.WarnWithMeta("dispatcher", fmt.Sprintf("跳过上游地址异常候选: stage=%d group=%s model=%s state=%s", stageIndex, stage.GroupName, statsModelID(model), bucketState), LogMeta{
					Model:    statsModelID(model),
					Metadata: map[string]interface{}{"stage": stageIndex, "stage_group": stage.GroupName, "candidate": candidateIndex, "upstream_bucket": bucketState},
				})
				continue
			}

			attempted++
			attemptsByGroup[candidateGroup]++
			route.Model = model
			bindUsageModel(pre, model)
			if pre != nil && pre.usage != nil {
				pre.usage.setFailoverCount(attempted - 1)
				pre.usage.setDispatchReason(dispatchSelectionReason(route, model, affinityModelID, capacityWaited, f.latency.isDegraded(affinityModelID, time.Now())))
			}
			imageCapability, capabilitySource := f.imageCaps.effective(model, time.Now())
			candidateView := candidateConversationForModel(pre, imageState, imageCapability)
			providerHasImage := candidateView.imageCount > 0
			logger.InfoWithMeta("dispatcher", fmt.Sprintf("尝试分组候选: stage=%d group=%s candidate=%d model=%s", stageIndex, stage.GroupName, candidateIndex, statsModelID(model)), LogMeta{
				Model: statsModelID(model),
				Metadata: map[string]interface{}{
					"stage":                           stageIndex,
					"stage_group":                     stage.GroupName,
					"candidate":                       candidateIndex,
					"has_image":                       providerHasImage,
					"image_count":                     candidateView.imageCount,
					"image_bytes":                     candidateView.imageBytes,
					"conversation_image_count":        imageCount,
					"conversation_image_bytes":        imageBytes,
					"current_image_count":             imageState.currentCount,
					"pending_history_image_count":     imageState.pendingHistoryCount,
					"projectable_history_image_count": imageState.projectableHistoryCount,
					"history_view":                    candidateView.historyView,
					"projected_image_count":           candidateView.projectedImageCount,
					"projected_image_bytes":           candidateView.projectedImageBytes,
					"capability_source":               capabilitySource,
				},
			})

			var contResult *converter.ResponsesContinuationResult
			var attemptResp *http.Response

			// 查找当前候选所属的真实分组配置
			actualCandidateGroup := route.Group
			if cg, ok := route.CandidateGroups[model.ID]; ok && cg != nil {
				actualCandidateGroup = cg
			}

			configuredFirstByteTimeout := time.Duration(actualCandidateGroup.FirstByteTimeoutMS) * time.Millisecond
			firstByteTimeout := imageAwareFirstByteTimeout(
				configuredFirstByteTimeout,
				deadline,
				time.Now(),
				f.latency.isDegraded(model.ID, time.Now()),
				providerHasImage,
				len(stageCandidates),
			)
			resp, firstByteDuration, err := requestWithProtocolFallback(req.Context(), firstByteTimeout, func(attemptCtx context.Context) (*http.Response, error) {
				var forwardErr error
				attemptResp, contResult, forwardErr = f.forwardCandidate(attemptCtx, candidateView.pre, model, logger, 0)
				return attemptResp, forwardErr
			}, func(attemptCtx context.Context, attemptResp *http.Response, remaining time.Duration) error {
				if !isSSEResponse(attemptResp) {
					return waitForUpstreamFirstByte(attemptResp, remaining)
				}
				return probeSSEPrecommit(attemptCtx, attemptResp, model.Provider, model.Endpoint, remaining, 0)
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
				} else if isUpstreamProtocolFailure(err) {
					failureClass = string(modelFailureUpstream)
					f.health.failure(model, modelFailureUpstream, 0, time.Now())
					f.upstream.neutral(model, time.Now())
					f.affinity.clear(route.Group.ID, conversationID, model.ID)
					markAvoidanceScopes(f.avoidance, scopes, model.ID, time.Now())
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
				logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选网络失败: stage=%d group=%s model=%s error=%v", stageIndex, stage.GroupName, statsModelID(model), err), LogMeta{
					Model: statsModelID(model),
					Metadata: map[string]interface{}{
						"stage":         stageIndex,
						"stage_group":   stage.GroupName,
						"failure_class": failureClass,
						"candidate":     candidateIndex,
					},
				})
				continue
			}
			resp.Body = &leasedUpstreamBody{ReadCloser: resp.Body, lease: lease}
			if firstByteDuration > 0 {
				f.latency.record(model.ID, firstByteDuration, configuredFirstByteTimeout, time.Now())
			}
			bodyDetail := ""
			capabilityMismatch := false
			if resp.StatusCode == http.StatusBadRequest {
				bodyDetail = preserveResponseBody(resp)
				capabilityMismatch = providerHasImage && isModelCapabilityMismatch(resp.StatusCode, bodyDetail)
			}
			if shouldFailoverModelStatus(resp.StatusCode) || capabilityMismatch {
				if bodyDetail == "" {
					bodyDetail = preserveResponseBody(resp)
				}
				failureClass := modelFailureForStatus(resp.StatusCode)
				if capabilityMismatch {
					failureClass = modelFailureCapability
					f.imageCaps.observe(model, modelImageUnsupported, time.Now())
					f.health.neutral(model)
					f.upstream.neutral(model, time.Now())
				} else {
					retryAfter := time.Duration(0)
					if failureClass == modelFailureRateLimit {
						retryAfter = modelRetryAfter(resp.Header.Get("Retry-After"), time.Now())
					}
					f.health.failure(model, failureClass, retryAfter, time.Now())
				}
				f.affinity.clear(route.Group.ID, conversationID, model.ID)
				if capabilityMismatch {
					markConversationAvoidance(f.avoidance, route.Group.ID, conversationID, model.ID, time.Now())
				} else {
					markAvoidanceScopes(f.avoidance, scopes, model.ID, time.Now())
				}
				lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
				if lastResp != nil {
					lastResp.Body.Close()
				}
				lastResp = resp
				lastContResult = contResult
				lastModel = model
				if pre != nil && pre.usage != nil {
					if capabilityMismatch {
						pre.usage.setFailoverReason("capability_mismatch")
					} else {
						pre.usage.setFailoverReason(fmt.Sprintf("http_%d", resp.StatusCode))
					}
				}
				logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选返回可切换错误: stage=%d group=%s model=%s status=%d", stageIndex, stage.GroupName, statsModelID(model), resp.StatusCode), LogMeta{
					Model:  statsModelID(model),
					Status: resp.StatusCode,
					Metadata: map[string]interface{}{
						"stage":         stageIndex,
						"stage_group":   stage.GroupName,
						"failure_class": string(failureClass),
						"candidate":     candidateIndex,
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
				if pre == nil || pre.Protocol != "kiro" {
					if providerHasImage {
						f.imageCaps.observe(model, modelImageSupported, time.Now())
					}
					f.upstream.recordSuccess(model, time.Now())
					f.health.success(model)
					f.affinity.set(route.Group.ID, conversationID, model.ID, time.Now())
					clearAvoidanceScopes(f.avoidance, scopes, model.ID)
					resp.Body = &observedUpstreamBody{
						ReadCloser: resp.Body,
						ctx:        req.Context(),
						onFailure: func(readErr error) {
							f.health.failure(model, modelFailureTransport, 0, time.Now())
							f.upstream.recordFailure(model, time.Now())
							f.affinity.clear(route.Group.ID, conversationID, model.ID)
							logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选响应流中断: stage=%d group=%s model=%s error=%v", stageIndex, stage.GroupName, statsModelID(model), readErr), LogMeta{
								Model: statsModelID(model),
								Metadata: map[string]interface{}{
									"stage":         stageIndex,
									"stage_group":   stage.GroupName,
									"failure_class": string(modelFailureTransport),
								},
							})
						},
					}
				}
			} else {
				f.health.neutral(model)
			}

			// 成功响应，直接返回（不继续尝试后续阶段）
			stageSuccess = true
			return f.finalizeForwardResponse(req.Context(), resp, pre, model, logger, contResult, func(outcome converter.StreamOutcome) {
				now := time.Now()
				failureClass := modelFailureTransport
				switch outcome {
				case converter.StreamOutcomeCompleted:
					if providerHasImage {
						f.imageCaps.observe(model, modelImageSupported, now)
					}
					f.upstream.recordSuccess(model, now)
					f.health.success(model)
					f.affinity.set(route.Group.ID, conversationID, model.ID, now)
					clearAvoidanceScopes(f.avoidance, scopes, model.ID)
					return
				case converter.StreamOutcomeClientCanceled:
					f.health.neutral(model)
					f.upstream.neutral(model, now)
					return
				case converter.StreamOutcomeUpstreamProtocolFailure:
					failureClass = modelFailureUpstream
				case converter.StreamOutcomeMissingTerminal, converter.StreamOutcomeReadFailure:
					f.upstream.recordFailure(model, now)
				}
				f.health.failure(model, failureClass, 0, now)
				f.affinity.clear(route.Group.ID, conversationID, model.ID)
				markAvoidanceScopes(f.avoidance, scopes, model.ID, now)
				logger.WarnWithMeta("dispatcher", fmt.Sprintf("候选流式终态异常: stage=%d group=%s model=%s outcome=%s", stageIndex, stage.GroupName, statsModelID(model), outcome), LogMeta{
					Model: statsModelID(model),
					Metadata: map[string]interface{}{
						"stage":          stageIndex,
						"stage_group":    stage.GroupName,
						"failure_class":  string(failureClass),
						"stream_outcome": string(outcome),
					},
				})
			})
		}

		// 如果阶段内成功，直接返回，不继续后续阶段
		if stageSuccess {
			break
		}
	}

	if lastResp != nil && lastModel != nil {
		route.Model = lastModel
		bindUsageModel(pre, lastModel)
		return f.finalizeForwardResponse(req.Context(), lastResp, pre, lastModel, logger, lastContResult, nil)
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

func markConversationAvoidance(store *modelAvoidanceStore, groupID, conversationID, modelID string, now time.Time) {
	if store == nil || groupID == "" || conversationID == "" {
		return
	}
	store.mark("turn:"+groupID+":"+conversationID, modelID, now)
}

func clearAvoidanceScopes(store *modelAvoidanceStore, scopes []string, modelID string) {
	for _, scope := range scopes {
		store.clear(scope, modelID)
	}
}

func (f *Forwarder) forwardCandidate(ctx context.Context, pre *Preflight, model *store.Model, logger *TrafficLogger, retryBudget time.Duration) (*http.Response, *converter.ResponsesContinuationResult, error) {
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

	f.logPrefixDrift(logger, pre, model, upstreamBody, contResult)
	logger.InfoBytes("forwarder", fmt.Sprintf("转发: %s → %s (body=%d bytes)", model.ModelID, upstreamURL, len(upstreamBody)), upstreamBody)
	sessionID := ""
	if pre != nil && pre.Conv != nil {
		if pre.Conv.IsClassifier {
			sessionID = "kiro-classifier"
		} else {
			sessionID = pre.Conv.ConversationID
		}
	}
	resp, err := f.sendCandidateRequest(ctx, upstreamURL, upstreamBody, model, logger, sessionID, pre.usage, retryBudget)
	if err != nil {
		return nil, contResult, err
	}

	if contResult != nil && contResult.UsedContinuation && resp.StatusCode >= 400 && resp.StatusCode < 500 {
		body, readErr := readUpstreamErrorBody(resp.Body)
		if readErr != nil {
			logger.Warn("forwarder", fmt.Sprintf("读取续传错误响应受限: %v", readErr))
		}
		bodyStr := string(body)
		if shouldRetryWithoutContinuation(bodyStr) {
			logger.Warn("forwarder", fmt.Sprintf("续传请求被上游拒绝，清理状态并回退全量: %s", truncate(bodyStr, 200)))
			if deleteErr := f.db.DeleteResponsesSession(continuationStateKey); deleteErr != nil {
				logger.Error("forwarder", fmt.Sprintf("清理 Responses 续传状态失败: %v", deleteErr))
			}
			upstreamBody = contResult.FallbackBody
			contResult.UsedContinuation = false
			resp, err = f.sendCandidateRequest(ctx, upstreamURL, upstreamBody, model, logger, sessionID, pre.usage, retryBudget)
			if err != nil {
				return nil, contResult, err
			}
		} else {
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return resp, contResult, nil
}

func (f *Forwarder) sendCandidateRequest(ctx context.Context, url string, body []byte, model *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder, retryBudget time.Duration) (*http.Response, error) {
	if retryBudget > 0 {
		return f.sendWithRetryBudget(ctx, url, body, model, logger, sessionID, usage, retryBudget)
	}
	return f.doRequest(ctx, url, body, model, sessionID, usage)
}

func (f *Forwarder) finalizeForwardResponse(ctx context.Context, resp *http.Response, pre *Preflight, model *store.Model, logger *TrafficLogger, contResult *converter.ResponsesContinuationResult, onStreamOutcome func(converter.StreamOutcome)) (*http.Response, error) {
	if pre != nil && pre.usage != nil {
		pre.usage.wrapResponse(resp)
	}
	if pre == nil || pre.Protocol != "kiro" {
		return resp, nil
	}
	return f.wrapKiroStream(ctx, resp, pre, model, logger, contResult, onStreamOutcome)
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
	body, _ := readUpstreamErrorBody(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	return truncate(string(body), 500)
}

func readUpstreamErrorBody(body io.ReadCloser) ([]byte, error) {
	return readUpstreamErrorBodyWithLimits(body, upstreamErrorBodyLimit, upstreamErrorBodyTimeout)
}

func readUpstreamErrorBodyWithLimits(body io.ReadCloser, limit int64, timeout time.Duration) ([]byte, error) {
	if body == nil {
		return nil, nil
	}

	timedOut := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		close(timedOut)
		_ = body.Close()
	})
	data, readErr := io.ReadAll(io.LimitReader(body, limit+1))
	timerStopped := timer.Stop()
	if timerStopped {
		_ = body.Close()
	} else {
		<-timedOut
	}

	if int64(len(data)) > limit {
		data = data[:limit]
		return data, errUpstreamErrorBodyTooLarge
	}
	if !timerStopped {
		return data, errUpstreamErrorBodyTimeout
	}
	return data, readErr
}

// sendWithRetry 发送请求，失败重试
func (f *Forwarder) sendWithRetry(ctx context.Context, url string, body []byte, m *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder) (*http.Response, error) {
	return f.sendWithRetryBudget(ctx, url, body, m, logger, sessionID, usage, minimumRetryBudget)
}

func (f *Forwarder) sendWithRetryBudget(ctx context.Context, url string, body []byte, m *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder, retryBudget time.Duration) (*http.Response, error) {
	if retryBudget <= 0 {
		retryBudget = minimumRetryBudget
	}
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

		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		budgetTimer := time.AfterFunc(remaining, cancelAttempt)
		resp, err := f.doRequest(attemptCtx, url, body, m, sessionID, usage)
		if err != nil {
			budgetTimer.Stop()
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
			budgetTimer.Stop()
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			logger.Warn("forwarder", fmt.Sprintf("上游返回 %d（第 %d/%d 次请求），准备重试", resp.StatusCode, attempt+1, maxRetries+1))
			continue
		}
		if shouldProbeSSEPrecommit(resp, body, m, url) {
			probeErr := probeSSEPrecommit(attemptCtx, resp, m.Provider, url, time.Until(deadline), 0)
			if probeErr != nil {
				if resp.Body != nil {
					_ = resp.Body.Close()
				}
				budgetTimer.Stop()
				cancelAttempt()
				lastErr = probeErr
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if !time.Now().Before(deadline) {
					lastErr = context.DeadlineExceeded
					break
				}
				logger.Warn("forwarder", fmt.Sprintf("上游流在首个有效事件前失败 (attempt %d): %v", attempt, probeErr))
				transportRetries++
				if transportRetries > maxTransportRetries {
					return nil, fmt.Errorf("上游失败（流式预提交重试 %d 次）: %w", maxTransportRetries, lastErr)
				}
				nextRetryDelay = transportRetryDelay
				continue
			}
		}
		if !budgetTimer.Stop() {
			if resp.Body != nil {
				_ = resp.Body.Close()
			} else {
				cancelAttempt()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = context.DeadlineExceeded
			break
		}
		return resp, nil
	}
	return nil, fmt.Errorf("上游失败（重试 %d 次）: %w", maxRetries, lastErr)
}

func shouldProbeSSEPrecommit(resp *http.Response, body []byte, model *store.Model, upstreamURL string) bool {
	if resp == nil || resp.Body == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false
	}
	if isSSEResponse(resp) {
		return true
	}
	var request struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &request) == nil && request.Stream {
		return true
	}
	provider := ""
	if model != nil {
		provider = strings.ToLower(strings.TrimSpace(model.Provider))
	}
	return provider == "codex" || provider == "gemini" && strings.Contains(strings.ToLower(upstreamURL), "streamgeneratecontent")
}

func isSSEResponse(resp *http.Response) bool {
	return resp != nil && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
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
	resp, err := client.Do(req)
	if err == nil {
		wrapUpstreamReadIdleTimeout(resp, upstreamReadIdleTimeout)
	}
	return resp, err
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

func (f *Forwarder) logPrefixDrift(logger *TrafficLogger, pre *Preflight, m *store.Model, body []byte, contResult *converter.ResponsesContinuationResult) {
	if logger == nil || pre == nil || pre.Conv == nil || pre.Conv.IsClassifier || m == nil {
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
		Contents           json.RawMessage `json:"contents"`
		Tools              json.RawMessage `json:"tools"`
		PreviousResponseID string          `json:"previous_response_id"`
	}
	providerInputJSON := ""
	if contResult != nil && contResult.FullInputJSON != "" {
		providerInputJSON = contResult.FullInputJSON
	}
	if json.Unmarshal(body, &auditBody) == nil {
		actualCacheKey = auditBody.PromptCacheKey
		previousResponseID = auditBody.PreviousResponseID
		if len(auditBody.Input) > 0 {
			api = "responses"
		}
		if providerInputJSON == "" {
			prefix := auditBody.Input
			if len(prefix) == 0 {
				prefix = auditBody.Messages
			}
			if len(prefix) == 0 {
				prefix = auditBody.Contents
			}
			if len(prefix) > 0 {
				providerInputJSON = string(prefix)
			}
		}
		if providerToolsHash == "" && len(auditBody.Tools) > 0 {
			providerToolsHash = converter.HashInput(string(auditBody.Tools))
		}
	}
	itemHashes, hashErr := converter.PrefixAuditItemHashes(providerInputJSON)
	if m.Provider == "anthropic" {
		itemHashes, hashErr = converter.PrefixAuditItemHashesIgnoringCacheControl(providerInputJSON)
	}
	if hashErr == nil && providerInputJSON != "" {
		providerInputHash = converter.HashInput(providerInputJSON)
		providerInputItems = len(itemHashes)
	} else {
		itemHashes = nil
	}
	if openAICache {
		expectedCacheKey = converter.PromptCacheKeyForAuditWithProviderToolsHash(
			pre.Conv,
			m.ModelID,
			api,
			providerToolsHash,
		)
	}

	shape := converter.PrefixAuditShape{
		SystemHash:    converter.HashInput(staticSystem),
		ToolsHash:     providerToolsHash,
		CacheKey:      actualCacheKey,
		ModelConfigID: m.ID,
		ModelID:       m.ModelID,
		Provider:      m.Provider,
		Endpoint:      m.Endpoint,
		API:           api,
		ItemHashes:    itemHashes,
	}
	diagnostics := converter.ComparePrefixAuditShapes(nil, shape)
	if f != nil {
		diagnostics = f.prefixAudit.compareAndStore(responsesContinuationStateKey(pre.Conv), shape)
	}
	changeReasons := strings.Join(diagnostics.ChangeReasons, "|")
	if changeReasons == "" {
		changeReasons = "none"
	}

	logger.Info("cache", fmt.Sprintf(
		"prefix_audit: model=%s model_config=%s endpoint=%s api=%s conv=%s body=%d system_static=%d system_dynamic=%d system_hash=%s env=%d question=%d history=%d tools=%d raw_tools_hash=%s provider_tools_hash=%s tool_invalid=%d tool_schema_invalid=%d tool_websearch_filtered=%d tool_desc_truncated=%d tool_over_limit=%d tool_results=%d tool_blocks=%d tool_nontext_blocks=%d cache_key=%s expected_key=%s provider_input_items=%d provider_input_hash=%s previous_response=%v continuation=%v previous_audit=%v input_relation=%s prefix_changed=%v change_reasons=%s stable_items=%d added_items=%d removed_items=%d first_changed_item=%d",
		statsModelID(m),
		m.ID,
		m.Endpoint,
		api,
		pre.Conv.ConversationID,
		len(body),
		len(staticSystem),
		len(dynamicSystem),
		shape.SystemHash,
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
		diagnostics.HasPrevious,
		diagnostics.InputRelation,
		diagnostics.PrefixChanged,
		changeReasons,
		diagnostics.StableItems,
		diagnostics.AddedItems,
		diagnostics.RemovedItems,
		diagnostics.FirstChangedItem,
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
