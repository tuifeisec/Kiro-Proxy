package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"kiro-proxy/store"
)

const (
	openAIChatRetryErrorBodyLimit   = 64 << 10
	openAIChatRetryErrorBodyTimeout = 3 * time.Second
)

// logOpenAIChatRequest records the exact outbound tool set without retaining
// messages, tool descriptions, parameter contents, or credentials.
func logOpenAIChatRequest(logger *TrafficLogger, model *store.Model, upstreamURL string, body []byte, traceID string, attempt int) {
	if logger == nil || !isOpenAIChatUpstream(model, upstreamURL) {
		return
	}
	metadata := map[string]interface{}{
		"trace_id":        traceID,
		"model_config_id": model.ID,
		"endpoint":        openAIChatPath(upstreamURL),
		"request_bytes":   len(body),
		"attempt":         attempt,
	}
	if summary, err := summarizeOpenAIChatTools(body); err == nil {
		metadata["tool_summary"] = summary
	} else {
		metadata["tool_diagnostic_error"] = err.Error()
	}
	logger.InfoWithMeta("openai_chat", "OpenAI Chat 出站工具诊断", LogMeta{
		Model:    statsModelID(model),
		Metadata: metadata,
	})
}

func openAIChatPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Path
}

func openAIChatTraceID(ctx context.Context) string {
	operation := upstreamRequestOperationFromContext(ctx)
	if operation == nil {
		return ""
	}
	return strings.TrimSpace(operation.requestID)
}

type openAIChatAttemptContextKey struct{}

func withOpenAIChatAttempt(ctx context.Context, attempt int) context.Context {
	return context.WithValue(ctx, openAIChatAttemptContextKey{}, attempt)
}

func openAIChatAttempt(ctx context.Context) int {
	attempt, _ := ctx.Value(openAIChatAttemptContextKey{}).(int)
	return attempt
}

func openAIChatErrorMetadata(info openAIChatGatewayError, traceID string, attempt int) map[string]interface{} {
	metadata := map[string]interface{}{
		"trace_id":          traceID,
		"outer_http_status": info.HTTPStatus,
	}
	if attempt > 0 {
		metadata["attempt"] = attempt
	}
	if info.Code != "" {
		metadata["outer_code"] = info.Code
	}
	if info.NestedCode != "" {
		metadata["nested_code"] = info.NestedCode
	}
	if info.ExtErrorStatusCode != 0 {
		metadata["inner_http_status"] = info.ExtErrorStatusCode
	}
	if info.ExtErrorType != "" {
		metadata["inner_type"] = info.ExtErrorType
	}
	if info.ExtErrorCode != "" {
		metadata["inner_code"] = info.ExtErrorCode
	}
	if info.RequestID != "" {
		metadata["upstream_request_id"] = info.RequestID
	}
	return metadata
}

// inspectOpenAIChatRetryError is called only for a status that would otherwise
// be retried. A confirmed request error is restored byte-for-byte for the
// normal downstream error path. Unreadable or oversized bodies retain the
// existing retry behavior.
func inspectOpenAIChatRetryError(resp *http.Response) (openAIChatGatewayError, bool, error) {
	if resp == nil {
		return openAIChatGatewayError{}, false, nil
	}
	body, readErr := readUpstreamErrorBodyWithLimits(resp.Body, openAIChatRetryErrorBodyLimit, openAIChatRetryErrorBodyTimeout)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	info := parseOpenAIChatGatewayError(resp.StatusCode, resp.Header, body)
	return info, readErr == nil && info.isDefinitiveOpenAIChatRequestError(), readErr
}

// observeOpenAIChatResponse records error details as the existing downstream
// path consumes the body. It never reads ahead or changes the response bytes.
func observeOpenAIChatResponse(resp *http.Response, logger *TrafficLogger, model *store.Model, traceID string, attempt int, requestBody []byte) {
	if resp == nil || logger == nil {
		return
	}
	if resp.StatusCode < http.StatusBadRequest || resp.Body == nil {
		info := parseOpenAIChatGatewayError(resp.StatusCode, resp.Header, nil)
		logger.InfoWithMeta("openai_chat", "OpenAI Chat 上游响应", LogMeta{
			Model: statsModelID(model), Status: resp.StatusCode,
			Metadata: openAIChatErrorMetadata(info, traceID, attempt),
		})
		return
	}
	resp.Body = &openAIChatErrorObserver{
		ReadCloser: resp.Body, logger: logger, model: model, status: resp.StatusCode,
		headers: resp.Header, traceID: traceID, attempt: attempt, requestBody: requestBody,
	}
}

type openAIChatErrorObserver struct {
	io.ReadCloser
	logger      *TrafficLogger
	model       *store.Model
	status      int
	headers     http.Header
	traceID     string
	attempt     int
	requestBody []byte
	mu          sync.Mutex
	body        []byte
	tooLarge    bool
	complete    bool
	once        sync.Once
}

func (o *openAIChatErrorObserver) Read(p []byte) (int, error) {
	n, err := o.ReadCloser.Read(p)
	o.mu.Lock()
	if n > 0 {
		remaining := openAIChatRetryErrorBodyLimit - len(o.body)
		if n > remaining {
			o.tooLarge = true
		}
		if remaining > 0 {
			keep := n
			if keep > remaining {
				keep = remaining
			}
			o.body = append(o.body, p[:keep]...)
		}
	}
	if err == io.EOF {
		o.complete = true
	}
	o.mu.Unlock()
	if err != nil {
		o.log()
	}
	return n, err
}

func (o *openAIChatErrorObserver) Close() error {
	o.log()
	return o.ReadCloser.Close()
}

func (o *openAIChatErrorObserver) log() {
	o.once.Do(func() {
		o.mu.Lock()
		body := o.body
		if !o.complete || o.tooLarge {
			body = nil
		}
		incomplete := !o.complete || o.tooLarge
		o.mu.Unlock()
		info := parseOpenAIChatGatewayError(o.status, o.headers, body)
		metadata := openAIChatErrorMetadata(info, o.traceID, o.attempt)
		if incomplete {
			metadata["inspection_incomplete"] = true
		}
		if !incomplete && (info.Code == "11129" || info.NestedCode == "11129") && o.logger.filePath != "" {
			if summary, err := summarizeOpenAIChatTools(o.requestBody); err == nil && summary.ToolCount > 0 {
				directory := filepath.Join(filepath.Dir(o.logger.filePath), "captures")
				path, captureErr := captureOpenAIChatToolParameters(o.requestBody, o.traceID, o.attempt, directory)
				if captureErr != nil {
					metadata["tool_capture_error"] = captureErr.Error()
				} else {
					metadata["tool_capture_path"] = path
				}
			}
		}
		o.logger.InfoWithMeta("openai_chat", "OpenAI Chat 上游响应", LogMeta{
			Model: statsModelID(o.model), Status: o.status, Metadata: metadata,
		})
	})
}

func openAIChatErrorMessage(statusCode int, status string, body []byte, info openAIChatGatewayError) string {
	if info.isDefinitiveOpenAIChatRequestError() {
		return fmt.Sprintf("上游网关返回 HTTP %d，内部请求参数校验返回 HTTP %d。\n\n错误原因：%s", statusCode, info.ExtErrorStatusCode, extractUpstreamErrorReason(body))
	}
	return buildUpstreamErrorMessage(statusCode, status, body)
}
