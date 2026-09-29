package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"net"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

const localCompatBodyLimit = 32 << 20

var (
	errLocalCompatStreamProtocol        = errors.New("upstream stream protocol failure")
	errLocalCompatStreamMissingTerminal = errors.New("upstream stream missing terminal event")
	errLocalCompatDownstreamWrite       = errors.New("downstream response write failure")
)

type localCompatAPI string

const (
	localCompatChat      localCompatAPI = "chat"
	localCompatResponses localCompatAPI = "responses"
	localCompatMessages  localCompatAPI = "messages"
)

type localCompatRequest struct {
	Model      string `json:"model"`
	Stream     *bool  `json:"stream"`
	ImageCount int    `json:"-"`
	ImageBytes int    `json:"-"`
}

func (s *Server) handleLocalOpenAIChat(w http.ResponseWriter, r *http.Request) {
	s.handleLocalCompat(w, r, localCompatChat)
}

func (s *Server) handleLocalOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	s.handleLocalCompat(w, r, localCompatResponses)
}

func (s *Server) handleLocalAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	s.handleLocalCompat(w, r, localCompatMessages)
}
func (s *Server) handleLocalCompat(w http.ResponseWriter, r *http.Request, api localCompatAPI) {
	if r.Method == http.MethodOptions {
		writeLocalCORSHeaders(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		writeLocalCompatError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.allowLocalCompatRequest(r) {
		writeLocalCompatError(w, http.StatusForbidden, "局域网访问未开启")
		return
	}
	if !s.requireLocalCompatLicense(w) {
		return
	}

	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, localCompatBodyLimit+1))
	if r.Body != nil {
		r.Body.Close()
	}
	if err != nil {
		writeLocalCompatError(w, http.StatusBadRequest, "read request body failed: "+err.Error())
		return
	}
	if len(body) > localCompatBodyLimit {
		writeLocalCompatError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}

	requestMeta, err := parseLocalCompatRequest(body)
	if err != nil {
		writeLocalCompatError(w, http.StatusBadRequest, err.Error())
		return
	}
	route := s.router.ResolveExact(requestMeta.Model)
	if route == nil || route.Model == nil {
		writeLocalCompatError(w, http.StatusNotFound, "no route for model: "+requestMeta.Model)
		return
	}
	clientWantsStream := requestMeta.Stream != nil && *requestMeta.Stream
	hasImage := requestMeta.ImageCount > 0
	usageRecord := newRequestUsageRecorder(s.db, start, route.Model, r.URL.Path, string(api))
	usageRecord.setRoute(route)
	// 上游会话只使用客户端提供的真实身份；内部缓存亲和键可能跨对话共享。
	sessionID := localCompatSessionID(r, body)
	conversationID := localCompatAffinityID(sessionID, body, api)
	affinityModelID := ""
	var scopes []string
	if route.Group != nil {
		affinityModelID = s.forwarder.affinity.get(route.Group.ID, conversationID, time.Now())
		scopes = dispatchAvoidanceScopes(route.Group.ID, conversationID, requestClientIP(r))
	}
	candidates := s.orderedLocalCompatCandidates(route, affinityModelID, scopes, time.Now())
	var skippedImageCandidates []skippedImageCandidate
	candidates, skippedImageCandidates = imageCompatibleCandidates(candidates, hasImage, s.forwarder.imageCaps, time.Now())
	groupName := requestMeta.Model
	groupID := ""
	if route.Group != nil {
		groupName = route.Group.Name
		groupID = route.Group.ID
	}
	for _, skipped := range skippedImageCandidates {
		s.logger.InfoWithMeta("dispatcher", fmt.Sprintf("本地兼容接口跳过不支持图片的候选: group=%s model=%s", groupName, statsModelID(skipped.model)), LogMeta{
			Model: statsModelID(skipped.model),
			Metadata: map[string]interface{}{
				"group_id":          groupID,
				"has_image":         true,
				"image_count":       requestMeta.ImageCount,
				"image_bytes":       requestMeta.ImageBytes,
				"capability_source": skipped.source,
			},
		})
	}
	if hasImage && len(candidates) == 0 {
		writeLocalCompatError(w, http.StatusBadRequest, "当前路由没有支持图片输入的可用模型")
		return
	}
	grouped := route.Group != nil
	attempted := 0
	attemptsByGroup := make(map[string]int)
	var m *store.Model
	var prepared *localCompatPreparedCandidate
	var resp *http.Response
	var lastResp *http.Response
	var lastPrepared *localCompatPreparedCandidate
	var lastModel *store.Model
	var lastErr error
	deadline := time.Now().Add(modelGroupRetryBudget(route))

	for index, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if grouped && !candidateHasBudget(deadline, time.Now()) {
			break
		}
		candidateGroup := routeGroupForCandidate(route, candidate)
		if grouped && attemptsByGroup[candidateGroup.ID] >= candidateGroup.MaxAttempts {
			continue
		}
		if route.Group != nil {
			allowed, circuitState := s.forwarder.health.allow(candidate, time.Now())
			if !allowed {
				usageRecord.setFailoverReason("circuit_" + circuitState)
				s.logger.Warn("dispatcher", fmt.Sprintf("本地兼容接口跳过熔断模型: group=%s model=%s state=%s", route.Group.Name, statsModelID(candidate), circuitState))
				continue
			}
		}
		if routeErr := validateLocalCompatRoute(api, candidate); routeErr != nil {
			if route.Group == nil {
				writeLocalCompatError(w, http.StatusBadRequest, routeErr.Error())
				return
			}
			s.forwarder.health.neutral(candidate)
			s.logger.Debug("dispatcher", fmt.Sprintf("跳过协议不兼容候选: group=%s model=%s api=%s", route.Group.Name, statsModelID(candidate), api))
			continue
		}

		candidatePrepared, prepareErr := s.prepareLocalCompatCandidate(body, api, candidate, clientWantsStream, sessionID)
		if prepareErr != nil {
			if grouped {
				s.forwarder.health.neutral(candidate)
			}
			writeLocalCompatError(w, http.StatusBadRequest, prepareErr.Error())
			return
		}
		capacityWait := time.Duration(0)
		if grouped && candidate.ID == affinityModelID {
			capacityWait = affinityCapacityWait
		}
		capacityStarted := time.Now()
		lease, acquired := s.forwarder.load.acquireWithWait(r.Context(), candidate.ID, route.CandidateLimits[candidate.ID], capacityWait)
		capacityWaited := time.Since(capacityStarted)
		if capacityWait > 0 {
			usageRecord.addCapacityWait(capacityWaited)
		}
		if !acquired {
			if grouped {
				s.forwarder.health.neutral(candidate)
			}
			if r.Context().Err() != nil {
				lastErr = r.Context().Err()
				break
			}
			usageRecord.setFailoverReason("capacity_full")
			s.logger.Info("dispatcher", fmt.Sprintf("本地兼容接口跳过并发已满候选: group=%s model=%s", route.Group.Name, statsModelID(candidate)))
			continue
		}
		if grouped {
			bucketAllowed, bucketState := s.forwarder.upstream.allow(candidate, time.Now())
			if !bucketAllowed {
				lease.release()
				s.forwarder.health.neutral(candidate)
				usageRecord.setFailoverReason("upstream_bucket_" + bucketState)
				s.logger.Warn("dispatcher", fmt.Sprintf("本地兼容接口跳过上游地址异常候选: group=%s model=%s state=%s", route.Group.Name, statsModelID(candidate), bucketState))
				continue
			}
		}
		route.Model = candidate
		usageRecord.setModel(candidate)
		usageRecord.setFailoverCount(attempted)
		usageRecord.setDispatchReason(dispatchSelectionReason(route, candidate, affinityModelID, capacityWaited, s.forwarder.latency.isDegraded(affinityModelID, time.Now())))
		attempted++
		if grouped {
			attemptsByGroup[candidateGroup.ID]++
		}
		s.logger.InfoBytes("local-compat", fmt.Sprintf("本地兼容接口转发: api=%s model=%s → %s", api, statsModelID(candidate), candidatePrepared.upstreamURL), candidatePrepared.upstreamBody)
		if grouped {
			configuredFirstByteTimeout := time.Duration(candidateGroup.FirstByteTimeoutMS) * time.Millisecond
			firstByteTimeout := imageAwareFirstByteTimeout(
				configuredFirstByteTimeout,
				deadline,
				time.Now(),
				s.forwarder.latency.isDegraded(candidate.ID, time.Now()),
				hasImage,
				len(candidates),
			)
			var firstByteDuration time.Duration
			resp, firstByteDuration, err = requestWithProtocolFallback(r.Context(), firstByteTimeout, func(attemptCtx context.Context) (*http.Response, error) {
				if isOpenAIChatUpstream(candidate, candidatePrepared.upstreamURL) {
					attempt := nextOpenAIChatAttempt(upstreamRequestOperationFromContext(attemptCtx))
					attemptCtx = withOpenAIChatAttempt(attemptCtx, attempt)
					logOpenAIChatRequest(s.logger, candidate, candidatePrepared.upstreamURL, candidatePrepared.upstreamBody, openAIChatTraceID(attemptCtx), attempt)
				}
				return s.forwarder.doRequest(attemptCtx, candidatePrepared.upstreamURL, candidatePrepared.upstreamBody, candidate, sessionID, usageRecord)
			}, func(attemptCtx context.Context, attemptResp *http.Response, remaining time.Duration) error {
				if !clientWantsStream || !isSSEResponse(attemptResp) {
					return waitForUpstreamFirstByte(attemptResp, remaining)
				}
				return probeSSEPrecommit(attemptCtx, attemptResp, candidate.Provider, candidatePrepared.upstreamURL, remaining, 0)
			})
			if err == nil && firstByteDuration > 0 && resp != nil && (resp.StatusCode >= http.StatusBadRequest || !isSSEResponse(resp)) {
				s.forwarder.latency.record(candidate.ID, firstByteDuration, configuredFirstByteTimeout, time.Now())
			} else if errors.Is(err, errFirstByteTimeout) {
				s.forwarder.latency.record(candidate.ID, firstByteTimeout, configuredFirstByteTimeout, time.Now())
			}
		} else {
			resp, err = s.forwarder.sendWithRetryBudget(r.Context(), candidatePrepared.upstreamURL, candidatePrepared.upstreamBody, candidate, s.logger, sessionID, usageRecord, singleModelFirstByteTimeout)
		}
		if err != nil {
			lease.release()
			lastErr = err
			if route.Group == nil || r.Context().Err() != nil {
				break
			}
			failureClass := "candidate_error"
			if errors.Is(err, errFirstByteTimeout) {
				failureClass = "first_byte_slow"
				s.forwarder.health.neutral(candidate)
				s.forwarder.upstream.neutral(candidate, time.Now())
				if s.forwarder.latency.isDegraded(candidate.ID, time.Now()) {
					s.forwarder.affinity.clear(route.Group.ID, conversationID, candidate.ID)
				}
			} else if isUpstreamProtocolFailure(err) {
				failureClass = string(modelFailureUpstream)
				s.forwarder.health.failure(candidate, modelFailureUpstream, 0, time.Now())
				s.forwarder.upstream.neutral(candidate, time.Now())
				s.forwarder.affinity.clear(route.Group.ID, conversationID, candidate.ID)
				markAvoidanceScopes(s.forwarder.avoidance, scopes, candidate.ID, time.Now())
			} else if isUpstreamTransportFailure(err) {
				failureClass = string(modelFailureTransport)
				s.forwarder.health.failure(candidate, modelFailureTransport, 0, time.Now())
				s.forwarder.upstream.recordFailure(candidate, time.Now())
				s.forwarder.affinity.clear(route.Group.ID, conversationID, candidate.ID)
				markAvoidanceScopes(s.forwarder.avoidance, scopes, candidate.ID, time.Now())
			} else {
				s.forwarder.health.neutral(candidate)
				s.forwarder.upstream.neutral(candidate, time.Now())
			}
			usageRecord.setFailoverReason(failureClass)
			s.logger.Warn("dispatcher", fmt.Sprintf("本地兼容候选网络失败: group=%s candidate=%d model=%s error=%v", route.Group.Name, index, statsModelID(candidate), err))
			continue
		}
		resp.Body = &leasedUpstreamBody{ReadCloser: resp.Body, lease: lease}
		if grouped && resp.Request != nil && isOpenAIChatUpstream(candidate, candidatePrepared.upstreamURL) {
			observeOpenAIChatResponse(resp, s.logger, candidate, openAIChatTraceID(resp.Request.Context()), openAIChatAttempt(resp.Request.Context()))
		}

		detail := ""
		capabilityMismatch := false
		if grouped && resp.StatusCode == http.StatusBadRequest {
			detail = preserveResponseBody(resp)
			capabilityMismatch = hasImage && isModelCapabilityMismatch(resp.StatusCode, detail)
		}
		if grouped && (shouldFailoverModelStatus(resp.StatusCode) || capabilityMismatch) {
			if detail == "" {
				detail = preserveResponseBody(resp)
			}
			failureClass := modelFailureForStatus(resp.StatusCode)
			if capabilityMismatch {
				failureClass = modelFailureCapability
				s.forwarder.imageCaps.observe(candidate, modelImageUnsupported, time.Now())
				s.forwarder.health.neutral(candidate)
				s.forwarder.upstream.neutral(candidate, time.Now())
			} else {
				retryAfter := time.Duration(0)
				if failureClass == modelFailureRateLimit {
					retryAfter = modelRetryAfter(resp.Header.Get("Retry-After"), time.Now())
				}
				s.forwarder.health.failure(candidate, failureClass, retryAfter, time.Now())
			}
			s.forwarder.affinity.clear(route.Group.ID, conversationID, candidate.ID)
			if capabilityMismatch {
				markConversationAvoidance(s.forwarder.avoidance, route.Group.ID, conversationID, candidate.ID, time.Now())
			} else {
				markAvoidanceScopes(s.forwarder.avoidance, scopes, candidate.ID, time.Now())
			}
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			if lastResp != nil {
				lastResp.Body.Close()
			}
			lastResp = resp
			lastPrepared = candidatePrepared
			lastModel = candidate
			if capabilityMismatch {
				usageRecord.setFailoverReason("capability_mismatch")
			} else {
				usageRecord.setFailoverReason(fmt.Sprintf("http_%d", resp.StatusCode))
			}
			if resp.Request != nil && isOpenAIChatUpstream(candidate, candidatePrepared.upstreamURL) {
				s.logger.WarnWithMeta("dispatcher", fmt.Sprintf("本地兼容候选切换: group=%s candidate=%d model=%s status=%d detail=%s", route.Group.Name, index, statsModelID(candidate), resp.StatusCode, detail), LogMeta{
					Model: statsModelID(candidate), Status: resp.StatusCode,
					Metadata: map[string]interface{}{"trace_id": openAIChatTraceID(resp.Request.Context()), "attempt": openAIChatAttempt(resp.Request.Context()), "candidate": index},
				})
			} else {
				s.logger.Warn("dispatcher", fmt.Sprintf("本地兼容候选切换: group=%s candidate=%d model=%s status=%d detail=%s", route.Group.Name, index, statsModelID(candidate), resp.StatusCode, detail))
			}
			resp = nil
			continue
		}
		if grouped && resp.StatusCode < http.StatusBadRequest {
			usageRecord.markFirstByte(time.Now())
		}

		if lastResp != nil {
			lastResp.Body.Close()
		}
		if route.Group != nil && resp.StatusCode >= http.StatusBadRequest {
			s.forwarder.health.neutral(candidate)
		}
		m = candidate
		prepared = candidatePrepared
		break
	}

	if resp == nil && lastResp != nil {
		resp = lastResp
		prepared = lastPrepared
		m = lastModel
	}
	if resp == nil || prepared == nil || m == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("没有与 %s 兼容的可用分组成员", api)
		}
		usageRecord.finish(http.StatusBadGateway, localCompatUsage{}, lastErr.Error())
		modelKey := statsModelID(route.Model)
		s.stats.RecordRequestFull(0, 0, 0, 0, modelKey, true)
		s.logger.ErrorWithMeta("local-compat", "转发失败: "+lastErr.Error(), LogMeta{Model: modelKey, LatencyMS: time.Since(start).Milliseconds()})
		writeLocalCompatError(w, http.StatusBadGateway, "upstream error: "+lastErr.Error())
		return
	}
	route.Model = m
	usageRecord.setModel(m)
	modelKey := statsModelID(m)
	responsesFallback := prepared.responsesFallback
	fallbackState := prepared.fallbackState
	aggregateCodex := prepared.aggregateCodex
	defer resp.Body.Close()
	latencyThreshold := time.Duration(0)
	if candidateGroup := routeGroupForCandidate(route, m); candidateGroup != nil {
		latencyThreshold = time.Duration(candidateGroup.FirstByteTimeoutMS) * time.Millisecond
	}
	usageRecord.wrapResponseForModel(resp, m, prepared.upstreamURL, s.forwarder.contentLatencyHook(usageRecord, m, latencyThreshold))

	var usage localCompatUsage
	var writeErr error
	var fallbackResponseID string
	var fallbackAssistant map[string]interface{}
	status := resp.StatusCode
	upstreamSuccessfulStatus := status < http.StatusBadRequest
	if aggregateCodex && status < http.StatusBadRequest {
		var responseBody []byte
		responseBody, usage, writeErr = aggregateResponsesSSE(resp.Body)
		if writeErr != nil {
			status = http.StatusBadGateway
			writeLocalCompatError(w, status, "invalid upstream responses stream: "+writeErr.Error())
		} else {
			w.Header().Set("Content-Type", "application/json")
			writeLocalCORSHeaders(w)
			w.WriteHeader(status)
			_, writeErr = w.Write(responseBody)
			writeErr = wrapLocalCompatDownstreamWriteError(writeErr)
		}
	} else if responsesFallback && status < http.StatusBadRequest {
		writeLocalCORSHeaders(w)
		if clientWantsStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(status)
			usage, fallbackResponseID, fallbackAssistant, writeErr = copyChatStreamAsResponsesWithState(r.Context(), w, resp.Body)
		} else {
			responseBody, convertedUsage, convertErr := readAndConvertChatResponse(resp.Body)
			usage = convertedUsage
			if convertErr != nil {
				status = http.StatusBadGateway
				writeLocalCompatError(w, status, convertErr.Error())
				writeErr = convertErr
			} else {
				fallbackResponseID, fallbackAssistant = fallbackAssistantFromResponses(responseBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, writeErr = w.Write(responseBody)
				writeErr = wrapLocalCompatDownstreamWriteError(writeErr)
			}
		}
	} else {
		copyLocalCompatHeaders(w.Header(), resp.Header)
		writeLocalCORSHeaders(w)
		w.WriteHeader(status)
		usage, writeErr = copyLocalCompatResponse(r.Context(), w, resp.Body, api)
	}
	if responsesFallback && status < http.StatusBadRequest && writeErr == nil {
		s.saveFallbackResponse(fallbackResponseID, fallbackState, fallbackAssistant)
	}

	clientCanceled := r.Context().Err() != nil || errors.Is(writeErr, errLocalCompatDownstreamWrite)
	isError := status >= http.StatusBadRequest || writeErr != nil && !clientCanceled
	errorMessage := ""
	if writeErr != nil && !clientCanceled {
		errorMessage = writeErr.Error()
	} else if status >= http.StatusBadRequest {
		errorMessage = fmt.Sprintf("HTTP %d", status)
	}
	if route.Group != nil && m != nil && upstreamSuccessfulStatus {
		now := time.Now()
		switch {
		case clientCanceled:
			s.forwarder.health.neutral(m)
			s.forwarder.upstream.neutral(m, now)
		case writeErr == nil:
			if hasImage {
				s.forwarder.imageCaps.observe(m, modelImageSupported, now)
			}
			s.forwarder.upstream.recordSuccess(m, now)
			s.forwarder.health.success(m)
			s.forwarder.affinity.set(route.Group.ID, conversationID, m.ID, now)
			clearAvoidanceScopes(s.forwarder.avoidance, scopes, m.ID)
		default:
			failureClass := modelFailureTransport
			if errors.Is(writeErr, errLocalCompatStreamProtocol) {
				failureClass = modelFailureUpstream
				s.forwarder.upstream.neutral(m, now)
			} else {
				s.forwarder.upstream.recordFailure(m, now)
			}
			s.forwarder.health.failure(m, failureClass, 0, now)
			s.forwarder.affinity.clear(route.Group.ID, conversationID, m.ID)
			markAvoidanceScopes(s.forwarder.avoidance, scopes, m.ID, now)
		}
	}
	usageRecord.finish(status, usage, errorMessage)
	credits := store.CalculateCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, m)
	s.stats.RecordRequestFullWithCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, credits, modelKey, isError)
	if writeErr != nil {
		s.logger.ErrorWithMeta("local-compat", "响应写回失败: "+writeErr.Error(), LogMeta{Model: modelKey, Status: status, LatencyMS: time.Since(start).Milliseconds()})
		return
	}
	s.logger.InfoWithMeta("local-compat", fmt.Sprintf("本地兼容接口完成: api=%s status=%d in=%d out=%d", api, status, usage.InputTokens, usage.OutputTokens), LogMeta{Model: modelKey, Status: status, LatencyMS: time.Since(start).Milliseconds()})
}

func (s *Server) orderedLocalCompatCandidates(route *router.Route, affinityModelID string, scopes []string, now time.Time) []*store.Model {
	if route == nil || route.Group == nil || len(route.Stages) == 0 {
		candidates := orderedRouteCandidates(route, affinityModelID, s.forwarder.avoidance, scopes, now)
		if route != nil && route.Group != nil {
			candidates = s.forwarder.latency.order(candidates, now)
			candidates = s.forwarder.upstream.order(candidates, now)
		}
		return candidates
	}

	candidates := make([]*store.Model, 0, len(route.Candidates))
	for _, stage := range route.Stages {
		if len(stage.Candidates) == 0 {
			continue
		}
		stageCandidates := orderedRouteCandidates(&router.Route{
			Model:      stage.Candidates[0],
			Candidates: stage.Candidates,
			Group:      route.Group,
		}, affinityModelID, s.forwarder.avoidance, scopes, now)
		stageCandidates = s.forwarder.latency.order(stageCandidates, now)
		stageCandidates = s.forwarder.upstream.order(stageCandidates, now)
		candidates = append(candidates, stageCandidates...)
	}
	return candidates
}

// localCompatSessionID 不从提示词或 prompt_cache_key 推测对话身份。
// 客户端未提供会话 ID 时留空，不能用全局常量或每次随机值冒充稳定会话。
func localCompatSessionID(r *http.Request, body []byte) string {
	if r != nil {
		for _, header := range []string{"X-Opencode-Session", "X-Conversation-ID", "X-Session-ID", "session_id", "conversation_id", "session-id"} {
			if value := strings.TrimSpace(r.Header.Get(header)); value != "" {
				return value
			}
		}
	}
	var payload struct {
		ConversationID string `json:"conversation_id"`
	}
	if json.Unmarshal(body, &payload) == nil {
		return strings.TrimSpace(payload.ConversationID)
	}
	return ""
}

func localCompatConversationID(r *http.Request, body []byte, api localCompatAPI) string {
	return localCompatAffinityID(localCompatSessionID(r, body), body, api)
}

func localCompatAffinityID(sessionID string, body []byte, api localCompatAPI) string {
	if sessionID != "" {
		return sessionID
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	for _, field := range []string{"prompt_cache_key"} {
		var value string
		if json.Unmarshal(payload[field], &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	stable := localCompatStableAffinityInput(payload, api)
	if len(stable) == 0 {
		return ""
	}
	hash := sha256.Sum256(stable)
	return "local-affinity-" + hex.EncodeToString(hash[:16])
}

func localCompatStableAffinityInput(payload map[string]json.RawMessage, api localCompatAPI) []byte {
	stable := make(map[string]interface{})
	for _, field := range []string{"tools"} {
		if value, ok := canonicalLocalCompatValue(payload[field]); ok {
			stable[field] = value
		}
	}

	switch api {
	case localCompatResponses:
		if value, ok := canonicalLocalCompatValue(payload["instructions"]); ok {
			stable["instructions"] = value
		}
	case localCompatChat:
		var messages []map[string]json.RawMessage
		if json.Unmarshal(payload["messages"], &messages) == nil {
			prefix := make([]interface{}, 0, len(messages))
			for _, message := range messages {
				var role string
				if json.Unmarshal(message["role"], &role) != nil || (role != "system" && role != "developer") {
					continue
				}
				if _, ok := canonicalLocalCompatValue(message["content"]); !ok {
					continue
				}
				encoded, err := json.Marshal(message)
				if err != nil {
					continue
				}
				if value, ok := canonicalLocalCompatValue(encoded); ok {
					prefix = append(prefix, value)
				}
			}
			if len(prefix) > 0 {
				stable["messages"] = prefix
			}
		}
	case localCompatMessages:
		if value, ok := canonicalLocalCompatValue(payload["system"]); ok {
			stable["system"] = value
		}
	}

	if len(stable) == 0 {
		return nil
	}
	stable["api"] = string(api)
	encoded, err := json.Marshal(stable)
	if err != nil {
		return nil
	}
	return encoded
}

func canonicalLocalCompatValue(raw json.RawMessage) (interface{}, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var value interface{}
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
	case []interface{}:
		if len(typed) == 0 {
			return nil, false
		}
	case map[string]interface{}:
		if len(typed) == 0 {
			return nil, false
		}
	}
	return value, true
}

type localCompatPreparedCandidate struct {
	upstreamBody      []byte
	upstreamURL       string
	responsesFallback bool
	fallbackState     *fallbackResponseState
	aggregateCodex    bool
}

func (s *Server) prepareLocalCompatCandidate(body []byte, api localCompatAPI, model *store.Model, clientWantsStream bool, sessionID string) (*localCompatPreparedCandidate, error) {
	prepared := &localCompatPreparedCandidate{}
	prepared.upstreamBody = replaceModelInBody(body, model.ModelID)
	prepared.responsesFallback = api == localCompatResponses && responsesNeedsChatFallback(model)
	if api == localCompatResponses {
		var err error
		prepared.upstreamBody, _, err = ensureResponsesPromptCacheKey(prepared.upstreamBody, model.ModelID)
		if err != nil {
			return nil, err
		}
	}
	if prepared.responsesFallback {
		previousMessages, err := s.fallbackMessages(responsesPreviousResponseID(prepared.upstreamBody), sessionID, model.ID, model.Provider)
		if err != nil {
			return nil, err
		}
		prepared.upstreamBody, prepared.fallbackState, err = responsesToChatFallbackWithPrevious(prepared.upstreamBody, model.ModelID, previousMessages)
		if err != nil {
			return nil, err
		}
		prepared.fallbackState.Provider = model.Provider
		prepared.fallbackState.ModelConfigID = model.ID
		prepared.fallbackState.SessionIDHash = sessionIDHash(sessionID)
	}
	prepared.aggregateCodex = api == localCompatResponses && model.Provider == "codex" && !clientWantsStream
	if api == localCompatResponses && model.Provider == "codex" {
		prepared.upstreamBody = codex.NewRequestBodyFromResponses(prepared.upstreamBody, model.ServiceTier)
	}
	var err error
	prepared.upstreamURL, err = localCompatUpstreamURL(api, model)
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (s *Server) allowLocalCompatRequest(r *http.Request) bool {
	if s.proxyAccessMode() == ProxyAccessLAN {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) requireLocalCompatLicense(w http.ResponseWriter) bool {
	result, err := s.licenseClient.CheckCached()
	if err != nil {
		writeLocalCompatError(w, http.StatusForbidden, "授权检测失败: "+err.Error())
		return false
	}
	if !result.Authorized {
		msg := "未授权，请加入群获取免费授权并在后台添加机器码"
		if result.Tampered {
			msg = "程序防篡改校验失败，请重新下载官方版本"
		}
		writeLocalCompatError(w, http.StatusForbidden, msg)
		return false
	}
	return true
}

func parseLocalCompatRequest(body []byte) (localCompatRequest, error) {
	var req localCompatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return req, fmt.Errorf("invalid JSON: %w", err)
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		return req, fmt.Errorf("model is required")
	}
	req.ImageCount, req.ImageBytes = localCompatImageStats(body)
	return req, nil
}

func localCompatImageStats(body []byte) (int, int) {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return 0, 0
	}

	count := 0
	bytes := 0

	var visitContent func(interface{})
	visitContent = func(value interface{}) {
		switch typed := value.(type) {
		case []interface{}:
			for _, item := range typed {
				visitContent(item)
			}
		case map[string]interface{}:
			blockType, _ := typed["type"].(string)
			switch strings.ToLower(strings.TrimSpace(blockType)) {
			case "image_url", "input_image":
				count++
				bytes += localCompatImageValueBytes(typed["image_url"])
			case "image":
				if source, ok := typed["source"].(map[string]interface{}); ok {
					count++
					bytes += localCompatImageValueBytes(source["data"])
				}
			}
			// 递归扫描 content 数组（OpenAI 多模态格式 / Anthropic 格式）
			if content, ok := typed["content"]; ok {
				visitContent(content)
			}
		}
	}

	// 只扫描协议规定的合法消息内容路径，不扫描 tools/metadata/schema：
	//   - /v1/chat/completions 和 /v1/messages：messages[].content[]
	//   - /v1/responses：input[].content[]（字段名是 input，不是 messages）
	for _, field := range []string{"messages", "input"} {
		items, ok := payload[field]
		if !ok {
			continue
		}
		itemArray, ok := items.([]interface{})
		if !ok {
			continue
		}
		for _, item := range itemArray {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if content, ok := itemMap["content"]; ok {
				visitContent(content)
			}
		}
	}

	return count, bytes
}

func localCompatImageValueBytes(value interface{}) int {
	switch typed := value.(type) {
	case string:
		if marker := strings.Index(typed, ";base64,"); marker >= 0 {
			return decodedBase64Length(typed[marker+8:])
		}
		lower := strings.ToLower(strings.TrimSpace(typed))
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			return 0
		}
		return decodedBase64Length(typed)
	case map[string]interface{}:
		return localCompatImageValueBytes(typed["url"])
	default:
		return 0
	}
}

func validateLocalCompatRoute(api localCompatAPI, m *store.Model) error {
	switch api {
	case localCompatChat:
		if m.Provider != "openai" {
			return fmt.Errorf("model %s is not an OpenAI chat route", statsModelID(m))
		}
	case localCompatResponses:
		if m.Provider != "openai" && m.Provider != "codex" {
			return fmt.Errorf("model %s is not an OpenAI responses route", statsModelID(m))
		}
	case localCompatMessages:
		if m.Provider != "anthropic" {
			return fmt.Errorf("model %s is not an Anthropic messages route", statsModelID(m))
		}
	}
	return nil
}

func localCompatUpstreamURL(api localCompatAPI, m *store.Model) (string, error) {
	baseURL := strings.TrimRight(m.BaseURL, "/")
	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("接口端点不能为空，请在模型配置中完整填写端点路径")
	}
	if m.DisableAutoSuffix {
		return strings.TrimSpace(m.BaseURL), nil
	}
	switch api {
	case localCompatChat:
		if strings.Contains(endpoint, "/responses") {
			endpoint = strings.Replace(endpoint, "/responses", "/chat/completions", 1)
		}
	case localCompatResponses:
		// OpenAI exposes both native endpoints. Derive /responses from a
		// chat-configured model so both local SDK entry points remain native.
		if strings.Contains(endpoint, "/chat/completions") {
			endpoint = strings.Replace(endpoint, "/chat/completions", "/responses", 1)
		}
	}
	return buildUpstreamURL(baseURL, endpoint), nil
}

func copyLocalCompatHeaders(dst, src http.Header) {
	dynamicHopByHop := make(map[string]struct{})
	for _, value := range src.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			dynamicHopByHop[strings.ToLower(strings.TrimSpace(token))] = struct{}{}
		}
	}
	for k, values := range src {
		key := http.CanonicalHeaderKey(k)
		lowerKey := strings.ToLower(key)
		if _, dynamic := dynamicHopByHop[lowerKey]; dynamic {
			continue
		}
		switch lowerKey {
		case "content-length", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "set-cookie":
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}

func writeLocalCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta, x-opencode-session, x-conversation-id, x-session-id, session_id, conversation_id, session-id")
}

func writeLocalCompatError(w http.ResponseWriter, status int, msg string) {
	writeLocalCORSHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": map[string]string{"message": msg, "type": "kiro_proxy_error"}})
}

type localCompatUsage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
}

type localCompatStreamWriter struct {
	dst                   http.ResponseWriter
	flusher               http.Flusher
	api                   localCompatAPI
	usage                 localCompatUsage
	pending               []byte
	fullJSON              bytes.Buffer
	isSSE                 bool
	eventType             string
	terminalSeen          bool
	failureSeen           bool
	downstreamWriteFailed bool
}

func copyLocalCompatResponse(ctx context.Context, w http.ResponseWriter, body io.Reader, api localCompatAPI) (localCompatUsage, error) {
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	tracker := &localCompatStreamWriter{dst: w, api: api, isSSE: strings.Contains(contentType, "text/event-stream")}
	tracker.flusher, _ = w.(http.Flusher)
	_, err := io.Copy(tracker, body)
	if err != nil && tracker.downstreamWriteFailed {
		err = fmt.Errorf("%w: %v", errLocalCompatDownstreamWrite, err)
	}
	if tracker.isSSE {
		tracker.consumeSSE(true)
		if err == nil {
			switch {
			case tracker.failureSeen:
				err = errLocalCompatStreamProtocol
			case !tracker.terminalSeen:
				err = errLocalCompatStreamMissingTerminal
			}
		}
		if err != nil && !tracker.failureSeen && !tracker.downstreamWriteFailed && (ctx == nil || ctx.Err() == nil) {
			if emitErr := tracker.emitControlledFailure(err); emitErr != nil {
				err = fmt.Errorf("%w; emit controlled failure: %v", err, emitErr)
			}
		}
	} else {
		mergeLocalCompatUsage(&tracker.usage, api, tracker.fullJSON.Bytes())
	}
	return tracker.usage, err
}

func wrapLocalCompatDownstreamWriteError(err error) error {
	if err == nil || errors.Is(err, errLocalCompatDownstreamWrite) {
		return err
	}
	return fmt.Errorf("%w: %v", errLocalCompatDownstreamWrite, err)
}

func (w *localCompatStreamWriter) Write(p []byte) (int, error) {
	if w.isSSE {
		w.pending = append(w.pending, p...)
		w.consumeSSE(false)
	} else if w.fullJSON.Len() < localCompatBodyLimit {
		remaining := localCompatBodyLimit - w.fullJSON.Len()
		if len(p) < remaining {
			remaining = len(p)
		}
		_, _ = w.fullJSON.Write(p[:remaining])
	}
	n, err := w.dst.Write(p)
	if err != nil {
		w.downstreamWriteFailed = true
	}
	if err == nil && w.flusher != nil {
		w.flusher.Flush()
	}
	return n, err
}

func (w *localCompatStreamWriter) consumeSSE(final bool) {
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(w.pending[:idx]))
		w.pending = w.pending[idx+1:]
		w.consumeSSELine(line)
	}
	if final && len(w.pending) > 0 {
		w.consumeSSELine(strings.TrimSpace(string(w.pending)))
		w.pending = nil
	}
}

func (w *localCompatStreamWriter) consumeSSELine(line string) {
	if strings.HasPrefix(line, "event:") {
		w.eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" {
		return
	}
	if data == "[DONE]" {
		if w.api == localCompatChat {
			w.terminalSeen = true
		}
		w.eventType = ""
		return
	}
	mergeLocalCompatUsage(&w.usage, w.api, []byte(data))
	w.observeSSETerminal(data)
}

func (w *localCompatStreamWriter) observeSSETerminal(data string) {
	var event struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return
	}
	eventType := event.Type
	if eventType == "" {
		eventType = w.eventType
	}
	w.eventType = ""
	if len(event.Error) > 0 && string(event.Error) != "null" {
		w.failureSeen = true
		w.terminalSeen = true
		return
	}
	switch w.api {
	case localCompatResponses:
		switch eventType {
		case "response.completed":
			w.terminalSeen = true
		case "response.failed", "response.incomplete", "error":
			w.failureSeen = true
			w.terminalSeen = true
		}
	case localCompatMessages:
		if eventType == "message_stop" {
			w.terminalSeen = true
		} else if eventType == "error" {
			w.failureSeen = true
			w.terminalSeen = true
		}
	case localCompatChat:
		for _, choice := range event.Choices {
			if choice.FinishReason != "" {
				w.terminalSeen = true
				break
			}
		}
	}
}

func (w *localCompatStreamWriter) emitControlledFailure(cause error) error {
	message := "upstream stream interrupted before completion"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	var eventType string
	var payload interface{}
	switch w.api {
	case localCompatResponses:
		eventType = "response.failed"
		payload = map[string]interface{}{
			"type": "response.failed",
			"response": map[string]interface{}{
				"status": "failed",
				"error":  map[string]string{"code": "upstream_stream_interrupted", "message": message},
			},
		}
	case localCompatMessages:
		eventType = "error"
		payload = map[string]interface{}{"type": "error", "error": map[string]string{"type": "api_error", "code": "upstream_stream_interrupted", "message": message}}
	default:
		payload = map[string]interface{}{"error": map[string]string{"type": "api_error", "code": "upstream_stream_interrupted", "message": message}}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	prefix := "data: "
	if eventType != "" {
		prefix = "event: " + eventType + "\ndata: "
	}
	if _, err := fmt.Fprintf(w.dst, "%s%s\n\n", prefix, encoded); err != nil {
		return err
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return nil
}

func mergeLocalCompatUsage(dst *localCompatUsage, api localCompatAPI, body []byte) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return
	}

	usageRaw := root["usage"]
	if api == localCompatMessages {
		if messageRaw := root["message"]; len(messageRaw) > 0 {
			var message map[string]json.RawMessage
			if json.Unmarshal(messageRaw, &message) == nil && len(message["usage"]) > 0 {
				usageRaw = message["usage"]
			}
		}
	} else if api == localCompatResponses {
		if responseRaw := root["response"]; len(responseRaw) > 0 {
			var response map[string]json.RawMessage
			if json.Unmarshal(responseRaw, &response) == nil && len(response["usage"]) > 0 {
				usageRaw = response["usage"]
			}
		}
	}
	mergeUsageObject(dst, usageRaw)
}

func mergeUsageObject(dst *localCompatUsage, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var usage struct {
		PromptTokens             int `json:"prompt_tokens"`
		CompletionTokens         int `json:"completion_tokens"`
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens    int `json:"prompt_cache_miss_tokens"`
		PromptTokensDetails      struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		InputTokensDetails struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"input_tokens_details"`
		CacheCreation struct {
			Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
			Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	}
	if json.Unmarshal(raw, &usage) != nil {
		return
	}
	input := maxInt(usage.PromptTokens, usage.InputTokens)
	if input == 0 && usage.PromptCacheHitTokens > 0 {
		input = usage.PromptCacheHitTokens + usage.PromptCacheMissTokens
	}
	output := maxInt(usage.CompletionTokens, usage.OutputTokens)
	cacheRead := maxInt(usage.CacheReadInputTokens, usage.PromptTokensDetails.CachedTokens, usage.InputTokensDetails.CachedTokens, usage.PromptCacheHitTokens)
	cacheWrite := maxInt(usage.CacheCreationInputTokens, usage.CacheCreation.Ephemeral5mInputTokens+usage.CacheCreation.Ephemeral1hInputTokens, usage.PromptTokensDetails.CacheWriteTokens, usage.InputTokensDetails.CacheWriteTokens)
	dst.InputTokens = maxInt(dst.InputTokens, input)
	dst.OutputTokens = maxInt(dst.OutputTokens, output)
	dst.CacheReadTokens = maxInt(dst.CacheReadTokens, cacheRead)
	dst.CacheWriteTokens = maxInt(dst.CacheWriteTokens, cacheWrite)
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

type responsesSSEAggregator struct {
	response  map[string]interface{}
	text      strings.Builder
	usage     localCompatUsage
	pending   []byte
	eventType string
	completed bool
	failure   string
}

func aggregateResponsesSSE(body io.Reader) ([]byte, localCompatUsage, error) {
	agg := &responsesSSEAggregator{}
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			agg.pending = append(agg.pending, buf[:n]...)
			agg.consume(false)
		}
		if err != nil {
			if err != io.EOF {
				return nil, agg.usage, err
			}
			break
		}
	}
	agg.consume(true)
	if agg.failure != "" {
		return nil, agg.usage, fmt.Errorf("%w: responses stream reported %s", errLocalCompatStreamProtocol, agg.failure)
	}
	if !agg.completed {
		return nil, agg.usage, fmt.Errorf("%w: response.completed", errLocalCompatStreamMissingTerminal)
	}
	if agg.response == nil {
		return nil, agg.usage, fmt.Errorf("upstream responses stream completed without response object")
	}
	if _, ok := agg.response["output_text"]; !ok && agg.text.Len() > 0 {
		agg.response["output_text"] = agg.text.String()
	}
	result, err := json.Marshal(agg.response)
	return result, agg.usage, err
}

func (a *responsesSSEAggregator) consume(final bool) {
	for {
		idx := bytes.IndexByte(a.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(a.pending[:idx]))
		a.pending = a.pending[idx+1:]
		a.consumeLine(line)
	}
	if final && len(a.pending) > 0 {
		a.consumeLine(strings.TrimSpace(string(a.pending)))
		a.pending = nil
	}
}

func (a *responsesSSEAggregator) consumeLine(line string) {
	if strings.HasPrefix(line, "event:") {
		a.eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		return
	}
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return
	}
	mergeLocalCompatUsage(&a.usage, localCompatResponses, []byte(data))

	var event struct {
		Type     string                 `json:"type"`
		Delta    string                 `json:"delta"`
		Response map[string]interface{} `json:"response"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return
	}
	eventType := event.Type
	if eventType == "" {
		eventType = a.eventType
	}
	switch eventType {
	case "response.output_text.delta":
		a.text.WriteString(event.Delta)
	case "response.completed":
		a.completed = true
		if event.Response != nil {
			a.response = event.Response
		}
	case "response.incomplete", "response.failed", "error":
		a.failure = eventType
	}
	a.eventType = ""
}
