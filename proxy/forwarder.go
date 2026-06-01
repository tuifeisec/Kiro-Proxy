// forwarder.go
// 转发逻辑：转换请求 → 发送上游 → 包装流式响应

package proxy

import (
	"bytes"
	"context"
	"fmt"
	json "github.com/goccy/go-json"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/ua"
	"kiro-proxy/upstreamproxy"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxRetries       = 2
	retryBaseDelay   = 200 * time.Millisecond
	defaultMaxTokens = 200000
)

// Forwarder 负责转发请求到上游
type Forwarder struct {
	stats     *store.Stats
	db        *store.DB
	clientMu  sync.RWMutex
	proxyAddr string
	client    *http.Client
}

// NewForwarder 创建转发器
func NewForwarder(stats *store.Stats, db *store.DB) *Forwarder {
	client, _ := upstreamproxy.NewClient("", 10*time.Minute)
	return &Forwarder{
		stats:  stats,
		db:     db,
		client: client,
	}
}

func (f *Forwarder) clientForRequest() (*http.Client, error) {
	proxyAddr, err := f.db.GetSetting(upstreamproxy.SettingKey)
	if err != nil {
		return nil, err
	}
	proxyAddr = strings.TrimSpace(proxyAddr)

	f.clientMu.RLock()
	if f.client != nil && f.proxyAddr == proxyAddr {
		client := f.client
		f.clientMu.RUnlock()
		return client, nil
	}
	f.clientMu.RUnlock()

	f.clientMu.Lock()
	defer f.clientMu.Unlock()
	if f.client != nil && f.proxyAddr == proxyAddr {
		return f.client, nil
	}

	client, err := upstreamproxy.NewClient(proxyAddr, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	f.proxyAddr = proxyAddr
	f.client = client
	return client, nil
}

// Forward 执行完整的转发流程
func (f *Forwarder) Forward(req *http.Request, pre *Preflight, route *router.Route, logger *TrafficLogger) (*http.Response, error) {
	m := route.Model

	// 转换请求体
	logger.Info("converter", fmt.Sprintf("开始转换: provider=%s, model_id=%s", m.Provider, m.ModelID))
	upstreamBody, upstreamURL, err := convertRequest(pre, m)
	if err != nil {
		return nil, fmt.Errorf("转换失败: %w", err)
	}

	// Responses API 续传优化：使用 previous_response_id 减少重发
	// 仅当模型配置启用了续传时才生效（默认关闭，因为很多中转站不支持）
	var contResult *converter.ResponsesContinuationResult
	if m.EnableContinuation && useResponsesAPI(pre, m) && pre.Conv != nil && !pre.Conv.IsClassifier && pre.Conv.ConversationID != "" {
		prevSession, _ := f.db.GetResponsesSession(pre.Conv.ConversationID)
		var prevState *converter.ResponsesContinuationState
		if prevSession != nil && prevSession.ResponseID != "" {
			prevState = &converter.ResponsesContinuationState{
				ConversationID: prevSession.ConversationID,
				ResponseID:     prevSession.ResponseID,
				InputJSON:      prevSession.InputJSON,
				ItemCount:      prevSession.ItemCount,
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
	resp, err := f.sendWithRetry(req.Context(), upstreamURL, upstreamBody, m, logger, sessionID)
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
			f.db.DeleteResponsesSession(pre.Conv.ConversationID)
			upstreamBody = contResult.FallbackBody
			contResult.UsedContinuation = false
			resp, err = f.sendWithRetry(req.Context(), upstreamURL, upstreamBody, m, logger, sessionID)
			if err != nil {
				return nil, err
			}
		} else {
			// 不是可安全归因于续传状态的 4xx，原样返回
			resp.Body = io.NopCloser(bytes.NewReader(body))
		}
	}

	// 非 Kiro 协议直接返回
	if pre.Protocol != "kiro" {
		return resp, nil
	}

	// Kiro 协议：包装流式转换器
	return f.wrapKiroStream(resp, pre, m, logger, contResult)
}

// useResponsesAPI 是请求/响应两侧唯一的"是否走 Responses API"判定口径。
// classifier（simple-task）请求必须走 chat/completions（上游返回可被分类器解析的纯文本），
// 绝不能走 responses。请求侧与响应侧必须使用同一判定，否则会出现请求按 chat 发、
// 响应却按 responses 解析（或反之）导致内容整段丢失。
func useResponsesAPI(pre *Preflight, m *store.Model) bool {
	if pre.Protocol != "kiro" || pre.Conv == nil {
		return false
	}
	if m.Provider == "anthropic" || m.Provider == "gemini" {
		return false
	}
	if pre.Conv.IsClassifier {
		return false
	}
	return strings.Contains(m.Endpoint, "/responses")
}

// convertRequest 将请求体转换为上游格式
func convertRequest(pre *Preflight, m *store.Model) ([]byte, string, error) {
	baseURL := strings.TrimRight(m.BaseURL, "/")
	endpoint := strings.TrimSpace(m.Endpoint)
	if endpoint == "" {
		return nil, "", fmt.Errorf("接口端点不能为空，请在模型配置中完整填写端点路径")
	}
	upstreamURL := baseURL + endpoint

	if pre.Protocol != "kiro" || pre.Conv == nil {
		return replaceModelInBody(pre.Body, m.ModelID), upstreamURL, nil
	}

	reasoningEffort := strings.TrimSpace(m.ReasoningEffort)

	// Kiro 协议：根据 provider + endpoint 选择转换器
	var body []byte
	var err error
	switch {
	case m.Provider == "anthropic":
		body, err = converter.KiroToAnthropic(pre.Conv, m.ModelID, reasoningEffort)
	case m.Provider == "gemini":
		body, err = converter.KiroToGemini(pre.Conv, m.ModelID, reasoningEffort)
		// Gemini URL: 替换 {model} 占位符，追加 ?alt=sse
		upstreamURL = strings.Replace(upstreamURL, "{model}", m.ModelID, 1)
		if !strings.Contains(upstreamURL, "alt=sse") {
			if strings.Contains(upstreamURL, "?") {
				upstreamURL += "&alt=sse"
			} else {
				upstreamURL += "?alt=sse"
			}
		}
	case useResponsesAPI(pre, m):
		body, err = converter.KiroToResponses(pre.Conv, m.ModelID, reasoningEffort)
	default: // chat/completions，或 classifier 请求
		if strings.Contains(endpoint, "/responses") {
			// 配置成 responses，但本次走 chat（classifier）：替换端点路径
			chatEndpoint := strings.Replace(endpoint, "/responses", "/chat/completions", 1)
			upstreamURL = baseURL + chatEndpoint
		}
		body, err = converter.KiroToOpenAI(pre.Conv, m.ModelID, reasoningEffort)
	}
	return body, upstreamURL, err
}

// wrapKiroStream 将上游 SSE 流包装为 Kiro AWS Event Stream
func (f *Forwarder) wrapKiroStream(resp *http.Response, pre *Preflight, m *store.Model, logger *TrafficLogger, contResult *converter.ResponsesContinuationResult) (*http.Response, error) {
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		logger.Error("forwarder", fmt.Sprintf("上游错误 %d", resp.StatusCode), truncate(string(body), 500))
		// 4xx/5xx 上游错误也需要计入统计（总请求数 + 错误数）
		f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	cw := m.ContextWindow
	if cw <= 0 {
		cw = defaultMaxTokens
	}

	modelID := ""
	if pre.Conv != nil {
		modelID = pre.Conv.ModelID
	}

	logger.Info("converter", fmt.Sprintf("开始流式转换: provider=%s endpoint=%s → Kiro Event Stream", m.Provider, m.Endpoint))

	capture := newStreamCapture()
	wrappedBody := capture.wrap(resp.Body)

	var streamBody io.ReadCloser
	switch {
	case m.Provider == "anthropic":
		sc := converter.NewAnthropicStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
		streamBody = sc
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[forwarder] panic in anthropic stats goroutine: %v", r)
					f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
				}
			}()
			in, out := sc.Usage()
			cacheR, cacheW := sc.CacheTokens()
			f.stats.RecordRequestFullWithCredits(in, out, cacheR, cacheW, store.CalculateCredits(in, out, cacheR, cacheW, m), statsModelID(m), false)
			logger.Info("forwarder", fmt.Sprintf("流式完成: model=%s in=%d out=%d cache_read=%d cache_write=%d", statsModelID(m), in, out, cacheR, cacheW), formatStreamCompletionDetail(resp, capture, in, out, cacheR, cacheW))
		}()
	case m.Provider == "gemini":
		sc := converter.NewGeminiStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
		streamBody = sc
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[forwarder] panic in gemini stats goroutine: %v", r)
					f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
				}
			}()
			in, out := sc.Usage()
			cacheR, _ := sc.CacheTokens()
			f.stats.RecordRequestFullWithCredits(in, out, cacheR, 0, store.CalculateCredits(in, out, cacheR, 0, m), statsModelID(m), false)
			logger.Info("forwarder", fmt.Sprintf("流式完成[gemini]: model=%s in=%d out=%d cache_read=%d", statsModelID(m), in, out, cacheR), formatStreamCompletionDetail(resp, capture, in, out, cacheR, 0))
		}()
	case useResponsesAPI(pre, m):
		// responses SSE → Kiro Event Stream（一段式直转）
		sc := converter.NewResponsesStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
		streamBody = sc
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[forwarder] panic in responses stats goroutine: %v", r)
					f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
				}
			}()
			in, out := sc.Usage()
			cacheR, _ := sc.CacheTokens()
			f.stats.RecordRequestFullWithCredits(in, out, cacheR, 0, store.CalculateCredits(in, out, cacheR, 0, m), statsModelID(m), false)
			logger.Info("forwarder", fmt.Sprintf("流式完成[responses]: model=%s in=%d out=%d cache_read=%d", statsModelID(m), in, out, cacheR), formatStreamCompletionDetail(resp, capture, in, out, cacheR, 0))

			// 保存 response_id 用于下次续传；只有明确完成且无错误的流才能更新续传状态。
			responseID := sc.ResponseID()
			if responseID != "" && sc.Succeeded() && contResult != nil && pre.Conv != nil && pre.Conv.ConversationID != "" {
				f.db.UpsertResponsesSession(&store.ResponsesSession{
					ConversationID: pre.Conv.ConversationID,
					InputHash:      converter.HashInput(contResult.FullInputJSON),
					ResponseID:     responseID,
					InputJSON:      contResult.FullInputJSON,
					ItemCount:      contResult.InputItemCount,
					UpdatedAt:      time.Now(),
				})
				ridShort := responseID
				if len(ridShort) > 12 {
					ridShort = ridShort[:12]
				}
				logger.Info("forwarder", fmt.Sprintf("续传状态已保存: convId=%s, responseId=%s", pre.Conv.ConversationID, ridShort))
			}
		}()
	default: // /v1/chat/completions（含 classifier）
		sc := converter.NewOpenAIStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
		streamBody = sc
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[forwarder] panic in openai stats goroutine: %v", r)
					f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
				}
			}()
			in, out := sc.Usage()
			cacheR, cacheW := sc.CacheTokens()
			f.stats.RecordRequestFullWithCredits(in, out, cacheR, cacheW, store.CalculateCredits(in, out, cacheR, cacheW, m), statsModelID(m), false)
			logger.Info("forwarder", fmt.Sprintf("流式完成: model=%s in=%d out=%d cache_read=%d cache_write=%d", statsModelID(m), in, out, cacheR, cacheW), formatStreamCompletionDetail(resp, capture, in, out, cacheR, cacheW))
		}()
	}

	resp.Body = streamBody
	resp.ContentLength = -1
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Set("Connection", "close")
	resp.Header.Del("Content-Length")
	resp.Header.Set("Cache-Control", "no-cache")

	return resp, nil
}

// sendWithRetry 发送请求，失败重试
func (f *Forwarder) sendWithRetry(ctx context.Context, url string, body []byte, m *store.Model, logger *TrafficLogger, sessionID string) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := retryBaseDelay * time.Duration(1<<(attempt-1))
			logger.Warn("forwarder", fmt.Sprintf("重试 %d/%d，等待 %v", attempt, maxRetries, delay))
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		resp, err := f.doRequest(ctx, url, body, m, sessionID)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			logger.Error("forwarder", fmt.Sprintf("请求失败 (attempt %d): %v", attempt, err))
			continue
		}

		if resp.StatusCode >= 500 && attempt < maxRetries {
			resp.Body.Close()
			lastErr = fmt.Errorf("上游 %d", resp.StatusCode)
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("上游失败（重试 %d 次）: %w", maxRetries, lastErr)
}

func (f *Forwarder) doRequest(ctx context.Context, url string, body []byte, m *store.Model, sessionID string) (*http.Response, error) {
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

	client, err := f.clientForRequest()
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func replaceModelInBody(body []byte, newModel string) []byte {
	if newModel == "" || len(body) == 0 {
		return body
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return body
	}
	parsed["model"] = newModel
	result, _ := json.Marshal(parsed)
	return result
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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

	logger.Info("cache", fmt.Sprintf(
		"prefix_audit: model=%s endpoint=%s conv=%s body=%d system_static=%d system_dynamic=%d env=%d question=%d history=%d tools=%d tools_hash=%s input_items=%d input_hash=%s continuation=%v",
		statsModelID(m),
		m.Endpoint,
		pre.Conv.ConversationID,
		len(body),
		len(staticSystem),
		len(dynamicSystem),
		len(envContext),
		len(currentQuestion),
		len(pre.Conv.History),
		len(pre.Conv.RawTools),
		pre.Conv.ToolsHash,
		inputItems,
		inputHash,
		contResult != nil && contResult.UsedContinuation,
	))
}

type streamCapture struct {
	buf bytes.Buffer
	max int // 最大缓冲字节数，超过后停止捕获
}

func newStreamCapture() *streamCapture {
	return &streamCapture{max: 512 * 1024} // 最多缓冲 512KB 用于日志
}

func (c *streamCapture) wrap(source io.ReadCloser) io.ReadCloser {
	return &captureReadCloser{source: source, writer: &c.buf, max: c.max}
}

func (c *streamCapture) String() string {
	return c.buf.String()
}

type captureReadCloser struct {
	source  io.ReadCloser
	writer  *bytes.Buffer
	max     int
	stopped bool
}

func (r *captureReadCloser) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	if n > 0 && !r.stopped {
		if r.writer.Len()+n > r.max {
			r.stopped = true // 超限后停止写入，避免无限增长
		} else {
			_, _ = r.writer.Write(p[:n])
		}
	}
	return n, err
}

func (r *captureReadCloser) Close() error {
	return r.source.Close()
}

func formatUpstreamResponseDetail(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	data := map[string]interface{}{
		"status":         resp.Status,
		"status_code":    resp.StatusCode,
		"proto":          resp.Proto,
		"content_length": resp.ContentLength,
		"headers":        sanitizedHeaders(resp.Header),
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	return string(b)
}

func formatStreamCompletionDetail(resp *http.Response, capture *streamCapture, in, out, cacheRead, cacheWrite int) string {
	data := map[string]interface{}{
		"usage": map[string]int{
			"input_tokens":       in,
			"output_tokens":      out,
			"cache_read_tokens":  cacheRead,
			"cache_write_tokens": cacheWrite,
		},
	}
	if resp != nil {
		data["upstream_response"] = map[string]interface{}{
			"status":         resp.Status,
			"status_code":    resp.StatusCode,
			"proto":          resp.Proto,
			"content_length": resp.ContentLength,
			"headers":        sanitizedHeaders(resp.Header),
		}
	}
	if capture != nil {
		data["upstream_stream"] = capture.String()
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	return string(b)
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
