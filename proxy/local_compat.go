package proxy

import (
	"bytes"
	"fmt"
	"io"
	"kiro-proxy/codex"
	"kiro-proxy/store"
	"net"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

const localCompatBodyLimit = 32 << 20

type localCompatAPI string

const (
	localCompatChat      localCompatAPI = "chat"
	localCompatResponses localCompatAPI = "responses"
	localCompatMessages  localCompatAPI = "messages"
)

type localCompatRequest struct {
	Model  string `json:"model"`
	Stream *bool  `json:"stream"`
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
	m := route.Model
	if err := validateLocalCompatRoute(api, m); err != nil {
		writeLocalCompatError(w, http.StatusBadRequest, err.Error())
		return
	}

	clientWantsStream := requestMeta.Stream != nil && *requestMeta.Stream
	upstreamBody := replaceModelInBody(body, m.ModelID)
	responsesFallback := api == localCompatResponses && responsesNeedsChatFallback(m)
	var fallbackState *fallbackResponseState
	if api == localCompatResponses {
		var cacheErr error
		upstreamBody, _, cacheErr = ensureResponsesPromptCacheKey(upstreamBody, m.ModelID)
		if cacheErr != nil {
			writeLocalCompatError(w, http.StatusBadRequest, cacheErr.Error())
			return
		}
	}
	if responsesFallback {
		previousMessages, previousErr := s.fallbackMessages(responsesPreviousResponseID(upstreamBody))
		if previousErr != nil {
			writeLocalCompatError(w, http.StatusBadRequest, previousErr.Error())
			return
		}
		var convertErr error
		upstreamBody, fallbackState, convertErr = responsesToChatFallbackWithPrevious(upstreamBody, m.ModelID, previousMessages)
		if convertErr != nil {
			writeLocalCompatError(w, http.StatusBadRequest, convertErr.Error())
			return
		}
	}
	aggregateCodex := api == localCompatResponses && m.Provider == "codex" && !clientWantsStream
	if api == localCompatResponses && m.Provider == "codex" {
		upstreamBody = codex.NewRequestBodyFromResponses(upstreamBody, m.ServiceTier)
	}
	upstreamURL, err := localCompatUpstreamURL(api, m)
	if err != nil {
		writeLocalCompatError(w, http.StatusBadRequest, err.Error())
		return
	}

	modelKey := statsModelID(m)
	usageRecord := newRequestUsageRecorder(s.db, start, m, r.URL.Path, string(api))
	s.logger.InfoBytes("local-compat", fmt.Sprintf("本地兼容接口转发: api=%s model=%s → %s", api, modelKey, upstreamURL), upstreamBody)
	resp, err := s.forwarder.sendWithRetry(r.Context(), upstreamURL, upstreamBody, m, s.logger, "local-compat", usageRecord)
	if err != nil {
		usageRecord.finish(http.StatusBadGateway, localCompatUsage{}, err.Error())
		s.stats.RecordRequestFull(0, 0, 0, 0, modelKey, true)
		s.logger.ErrorWithMeta("local-compat", "转发失败: "+err.Error(), LogMeta{Model: modelKey, LatencyMS: time.Since(start).Milliseconds()})
		writeLocalCompatError(w, http.StatusBadGateway, "upstream error: "+err.Error())
		return
	}
	defer resp.Body.Close()
	usageRecord.wrapResponse(resp)

	var usage localCompatUsage
	var writeErr error
	var fallbackResponseID string
	var fallbackAssistant map[string]interface{}
	status := resp.StatusCode
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
		}
	} else if responsesFallback && status < http.StatusBadRequest {
		writeLocalCORSHeaders(w)
		if clientWantsStream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(status)
			usage, fallbackResponseID, fallbackAssistant, writeErr = copyChatStreamAsResponsesWithState(w, resp.Body)
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
			}
		}
	} else {
		copyLocalCompatHeaders(w.Header(), resp.Header)
		writeLocalCORSHeaders(w)
		w.WriteHeader(status)
		usage, writeErr = copyLocalCompatResponse(w, resp.Body, api)
	}
	if responsesFallback && status < http.StatusBadRequest && writeErr == nil {
		s.saveFallbackResponse(fallbackResponseID, fallbackState, fallbackAssistant)
	}

	isError := status >= http.StatusBadRequest || writeErr != nil
	errorMessage := ""
	if writeErr != nil {
		errorMessage = writeErr.Error()
	} else if status >= http.StatusBadRequest {
		errorMessage = fmt.Sprintf("HTTP %d", status)
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
	return req, nil
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
	for k, values := range src {
		key := http.CanonicalHeaderKey(k)
		switch strings.ToLower(key) {
		case "content-length", "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
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
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, anthropic-version, anthropic-beta")
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
	dst      http.ResponseWriter
	flusher  http.Flusher
	api      localCompatAPI
	usage    localCompatUsage
	pending  []byte
	fullJSON bytes.Buffer
	isSSE    bool
}

func copyLocalCompatResponse(w http.ResponseWriter, body io.Reader, api localCompatAPI) (localCompatUsage, error) {
	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	tracker := &localCompatStreamWriter{dst: w, api: api, isSSE: strings.Contains(contentType, "text/event-stream")}
	tracker.flusher, _ = w.(http.Flusher)
	_, err := io.Copy(tracker, body)
	if tracker.isSSE {
		tracker.consumeSSE(true)
	} else {
		mergeLocalCompatUsage(&tracker.usage, api, tracker.fullJSON.Bytes())
	}
	return tracker.usage, err
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
	if !strings.HasPrefix(line, "data:") {
		return
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return
	}
	mergeLocalCompatUsage(&w.usage, w.api, []byte(data))
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
	case "response.completed", "response.incomplete", "response.failed":
		if event.Response != nil {
			a.response = event.Response
		}
	}
	a.eventType = ""
}
