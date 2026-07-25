// forwarder.go
// 转发逻辑：转换请求 → 发送上游 → 包装流式响应

package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"kiro-proxy/upstreamproxy"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	json "github.com/goccy/go-json"
)

const (
	maxRetries     = 1
	retryBaseDelay = 200 * time.Millisecond
	// 两次请求各允许等待 60 秒，并预留重试间隔和少量调度开销。
	retryBudget      = 125 * time.Second
	defaultMaxTokens = 200000
)

// Forwarder 负责转发请求到上游
type Forwarder struct {
	stats    *store.Stats
	db       *store.DB
	clientMu sync.RWMutex
	clients  map[string]*http.Client
}

// NewForwarder 创建转发器
func NewForwarder(stats *store.Stats, db *store.DB) *Forwarder {
	client, _ := upstreamproxy.NewClient("", 10*time.Minute)
	clients := make(map[string]*http.Client)
	if client != nil {
		clients[""] = client
	}
	return &Forwarder{
		stats:   stats,
		db:      db,
		clients: clients,
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

// Forward 执行完整的转发流程
func (f *Forwarder) Forward(req *http.Request, pre *Preflight, route *router.Route, logger *TrafficLogger) (*http.Response, error) {
	m := route.Model

	// 转换请求体
	logger.Info("converter", fmt.Sprintf("开始转换: provider=%s, model_id=%s", m.Provider, m.ModelID))
	conversionStarted := time.Now()
	upstreamBody, upstreamURL, err := convertRequest(pre, m)
	if pre != nil && pre.usage != nil {
		pre.usage.setConversionDuration(conversionStarted)
	}
	if err != nil {
		return nil, fmt.Errorf("转换失败: %w", err)
	}

	// Responses API 续传优化：使用 previous_response_id 减少重发
	// 仅当模型配置启用了续传时才生效（默认关闭，因为很多中转站不支持）
	var contResult *converter.ResponsesContinuationResult
	continuationStateKey := ""
	if pre != nil {
		continuationStateKey = responsesContinuationStateKeyFor(pre, m, resolveReasoningEffort(pre, m))
	}
	if m.EnableContinuation && useResponsesAPI(pre, m) && pre.Conv != nil && !pre.Conv.IsClassifier && continuationStateKey != "" {
		prevSession, sessionErr := f.db.GetResponsesSession(continuationStateKey)
		if sessionErr != nil {
			return nil, fmt.Errorf("读取 Responses 续传状态失败: %w", sessionErr)
		}
		var prevState *converter.ResponsesContinuationState
		if prevSession != nil && prevSession.ResponseID != "" {
			prevState = &converter.ResponsesContinuationState{
				ResponseID: prevSession.ResponseID,
				InputJSON:  prevSession.InputJSON,
				ItemCount:  prevSession.ItemCount,
			}
		}
		contResult, err = converter.ApplyResponsesContinuation(upstreamBody, prevState)
		if err == nil && contResult != nil {
			if contResult.UsedContinuation {
				prevID := prevState.ResponseID
				if len(prevID) > 12 {
					prevID = prevID[:12]
				}
				logger.Info("forwarder", fmt.Sprintf("续传命中: convId=%s, prev_response=%s, delta_size=%d bytes",
					pre.Conv.ConversationID, prevID, len(contResult.Body)))
			}
			upstreamBody = contResult.Body
		}
	}

	logPrefixDrift(logger, pre, m, upstreamBody, contResult)
	logger.InfoBytes("forwarder", fmt.Sprintf("转发: %s → %s (body=%d bytes)", m.ModelID, upstreamURL, len(upstreamBody)), upstreamBody)

	// 发送请求（带重试）
	sessionID := ""
	if pre.Conv != nil {
		if pre.Conv.IsClassifier {
			sessionID = "kiro-classifier"
		} else {
			sessionID = pre.Conv.ConversationID
		}
	}
	resp, err := f.sendWithRetry(req.Context(), upstreamURL, upstreamBody, m, logger, sessionID, pre.usage)
	if err != nil {
		return nil, err
	}

	// 续传被拒时回退全量发送。兼容 OpenAI/DeepSeek/sub2api 常见错误：
	// invalid_previous_response_id / previous_response_not_found / unsupported field 等。
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
			resp, err = f.sendWithRetry(req.Context(), upstreamURL, upstreamBody, m, logger, sessionID, pre.usage)
			if err != nil {
				return nil, err
			}
		} else {
			// 不是可安全归因于续传状态的 4xx，原样返回
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}

	if pre != nil && pre.usage != nil {
		pre.usage.wrapResponse(resp)
	}

	// 非 Kiro 协议直接返回
	if pre.Protocol != "kiro" {
		return resp, nil
	}

	// Kiro 协议：包装流式转换器
	return f.wrapKiroStream(resp, pre, m, logger, contResult)
}

// sendWithRetry 发送请求，失败重试
func (f *Forwarder) sendWithRetry(ctx context.Context, url string, body []byte, m *store.Model, logger *TrafficLogger, sessionID string, usage *requestUsageRecorder) (*http.Response, error) {
	lastErr := context.DeadlineExceeded
	deadline := time.Now().Add(retryBudget)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(1<<(attempt-1))
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

		resp, err := f.doRequest(ctx, url, body, m, sessionID, usage)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logger.Error("forwarder", fmt.Sprintf("请求失败 (attempt %d): %v", attempt, err))
			continue
		}

		if shouldRetryUpstreamStatus(resp.StatusCode) && attempt < maxRetries {
			if resp.ContentLength >= 0 && resp.ContentLength <= 32*1024 {
				_, _ = io.Copy(io.Discard, resp.Body)
			}
			resp.Body.Close()
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("上游失败（重试 %d 次）: %w", maxRetries, lastErr)
}

func shouldRetryUpstreamStatus(status int) bool {
	return status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
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
