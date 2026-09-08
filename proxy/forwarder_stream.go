package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/store"
	"log"
	"net/http"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

// wrapKiroStream 将上游 SSE 流包装为 Kiro AWS Event Stream
func (f *Forwarder) wrapKiroStream(ctx context.Context, resp *http.Response, pre *Preflight, m *store.Model, logger *TrafficLogger, contResult *converter.ResponsesContinuationResult, onStreamOutcome func(converter.StreamOutcome)) (*http.Response, error) {
	if resp.StatusCode >= 400 {
		body, readErr := readUpstreamErrorBody(resp.Body)
		if readErr != nil {
			logger.Warn("forwarder", fmt.Sprintf("读取上游错误响应受限: %v", readErr))
		}
		logger.ErrorWithMeta("forwarder", fmt.Sprintf("上游错误 %d", resp.StatusCode), LogMeta{
			Model:     statsModelID(m),
			Status:    resp.StatusCode,
			LatencyMS: time.Since(pre.StartTime).Milliseconds(),
		}, truncate(string(body), 500))
		// 4xx/5xx 上游错误也需要计入统计（总请求数 + 错误数）
		f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(m), true)
		pre.usage.finish(resp.StatusCode, localCompatUsage{}, fmt.Sprintf("HTTP %d", resp.StatusCode))
		return buildKiroTextStreamResponse(reqFromResponse(resp), buildUpstreamErrorMessage(resp.StatusCode, resp.Status, body), kiroModelID(pre, m), true), nil
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
	responseMetadata := upstreamMetadataFromResponse(resp)
	upstreamProtocol := responseMetadata.Protocol

	capture := newStreamCapture(logger.DetailLimit())
	wrappedBody := capture.wrap(resp.Body)

	var streamBody kiroStreamConverter
	streamLabel := ""
	var onCompleted func()
	switch {
	case m.Provider == "anthropic":
		streamBody = converter.NewAnthropicStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
	case m.Provider == "gemini":
		streamLabel = "gemini"
		streamBody = converter.NewGeminiStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
	case upstreamProtocol != "chat" && (useResponsesAPI(pre, m) || m.Provider == "codex"):
		// responses SSE → Kiro Event Stream（一段式直转）
		sc := converter.NewResponsesStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
		streamBody = sc
		streamLabel = "responses"
		onCompleted = func() {
			// 保存 response_id 用于下次续传；只有明确完成且无错误的流才能更新续传状态。
			responseID := sc.ResponseID()
			stateKey := responsesContinuationStateKeyFor(pre, m, resolveReasoningEffort(pre, m))
			if responseID != "" && contResult != nil && stateKey != "" {
				if err := f.db.UpsertResponsesSession(&store.ResponsesSession{
					StateKey:   stateKey,
					InputHash:  converter.HashInput(contResult.FullInputJSON),
					ResponseID: responseID,
					InputJSON:  contResult.FullInputJSON,
					ItemCount:  contResult.InputItemCount,
					UpdatedAt:  time.Now(),
				}); err != nil {
					logger.Error("forwarder", fmt.Sprintf("保存 Responses 续传状态失败: %v", err))
					return
				}
				ridShort := responseID
				if len(ridShort) > 12 {
					ridShort = ridShort[:12]
				}
				stateScope := "conversation"
				if stateKey != pre.Conv.ConversationID {
					stateScope = "branch"
				}
				logger.Info("forwarder", fmt.Sprintf("续传状态已保存: convId=%s, stateScope=%s, responseId=%s", pre.Conv.ConversationID, stateScope, ridShort))
			}
		}
	default: // /v1/chat/completions（含 classifier）
		streamBody = converter.NewOpenAIStreamConverter(wrappedBody, modelID, cw, m.InputPrice, m.OutputPrice, m.CacheInputPrice, m.CacheOutputPrice, m.RateMultiplier)
	}
	if len(responseMetadata.CompatibilityHistory) > 0 {
		previousCompleted := onCompleted
		onCompleted = func() {
			if previousCompleted != nil {
				previousCompleted()
			}
			if err := f.persistReasoningCompatibilityHistory(pre, m, responseMetadata.CompatibilityHistory); err != nil {
				logger.Error("forwarder", fmt.Sprintf("保存推理协议兼容状态失败: %v", err))
			}
		}
	}
	if source, ok := streamBody.(interface {
		ReasoningTrace() converter.ReasoningTrace
	}); ok {
		previousCompleted := onCompleted
		onCompleted = func() {
			if previousCompleted != nil {
				previousCompleted()
			}
			trace := source.ReasoningTrace()
			if strings.TrimSpace(trace.Text) == "" || len(trace.ToolCallIDs) == 0 {
				return
			}
			if err := f.saveReasoningTrace(pre, m, trace); err != nil {
				logger.Error("forwarder", fmt.Sprintf("保存推理回放状态失败: %v", err))
				return
			}
			logger.Info("forwarder", fmt.Sprintf("推理回放状态已保存: protocol=%s tool_calls=%d", trace.Protocol, len(trace.ToolCallIDs)))
		}
	}
	observationDone := make(chan struct{})
	go func() {
		defer close(observationDone)
		f.observeKiroStream(streamBody, streamLabel, resp, pre, m, logger, capture, onStreamOutcome, onCompleted)
	}()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = streamBody.Close()
			case <-observationDone:
			}
		}()
	}

	resp.Body = &completionObservedReadCloser{ReadCloser: streamBody, observed: observationDone}
	resp.ContentLength = -1
	resp.Header.Set("Content-Type", "application/vnd.amazon.eventstream")
	resp.Header.Set("Transfer-Encoding", "chunked")
	resp.Header.Del("Connection")
	resp.Header.Del("Content-Length")
	resp.Header.Set("Cache-Control", "no-cache")

	return resp, nil
}

type completionObservedReadCloser struct {
	io.ReadCloser
	observed <-chan struct{}
}

func (r *completionObservedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF && r.observed != nil {
		<-r.observed
	}
	return n, err
}

type kiroStreamConverter interface {
	io.ReadCloser
	Usage() (int, int)
	CacheTokens() (int, int)
	Outcome() converter.StreamOutcome
	Error() error
}

func (f *Forwarder) observeKiroStream(sc kiroStreamConverter, label string, resp *http.Response, pre *Preflight, model *store.Model, logger *TrafficLogger, capture *streamCapture, onOutcome func(converter.StreamOutcome), onCompleted func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[forwarder] panic in stream stats goroutine: %v", recovered)
			f.stats.RecordRequestFull(0, 0, 0, 0, statsModelID(model), true)
		}
	}()
	in, out := sc.Usage()
	cacheR, cacheW := sc.CacheTokens()
	outcome := sc.Outcome()
	credits := store.CalculateCredits(in, out, cacheR, cacheW, model)
	isUpstreamError := outcome != converter.StreamOutcomeCompleted && outcome != converter.StreamOutcomeClientCanceled
	f.stats.RecordRequestFullWithCredits(in, out, cacheR, cacheW, credits, statsModelID(model), isUpstreamError)
	errorMessage := ""
	if err := sc.Error(); err != nil && outcome != converter.StreamOutcomeClientCanceled {
		errorMessage = err.Error()
	}
	pre.usage.finish(resp.StatusCode, localCompatUsage{InputTokens: in, OutputTokens: out, CacheReadTokens: cacheR, CacheWriteTokens: cacheW}, errorMessage)
	if onOutcome != nil {
		onOutcome(outcome)
	}
	prefix := "流式完成"
	if outcome == converter.StreamOutcomeClientCanceled {
		prefix = "流式取消"
	} else if outcome != converter.StreamOutcomeCompleted {
		prefix = "流式失败"
	}
	if label != "" {
		prefix += "[" + label + "]"
	}
	logger.InfoWithMetaLazyDetail("forwarder", fmt.Sprintf("%s: model=%s outcome=%s in=%d out=%d cache_read=%d cache_write=%d", prefix, statsModelID(model), outcome, in, out, cacheR, cacheW), streamLogMeta(pre, model, resp, in, out, cacheR, cacheW, credits), func(maxBytes int) string {
		return formatStreamCompletionDetail(resp, capture, in, out, cacheR, cacheW, maxBytes)
	})
	if outcome == converter.StreamOutcomeCompleted && onCompleted != nil {
		onCompleted()
	}
}

type streamCapture struct {
	buf bytes.Buffer
	max int // 最大缓冲字节数，超过后停止捕获
}

func newStreamCapture(maxBytes int) *streamCapture {
	if maxBytes <= 0 {
		maxBytes = 512 * 1024
	}
	return &streamCapture{max: maxBytes}
}

func (c *streamCapture) wrap(source io.ReadCloser) io.ReadCloser {
	return &captureReadCloser{source: source, writer: &c.buf, max: c.max}
}

func (c *streamCapture) String(maxBytes int) string {
	if maxBytes > 0 && c.buf.Len() > maxBytes {
		return string(c.buf.Bytes()[:maxBytes]) + fmt.Sprintf("\n... [truncated upstream stream: %d -> %d bytes]", c.buf.Len(), maxBytes)
	}
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

func formatStreamCompletionDetail(resp *http.Response, capture *streamCapture, in, out, cacheRead, cacheWrite int, maxBytes int) string {
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
		data["upstream_stream"] = capture.String(maxBytes)
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	return string(b)
}
