package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"kiro-proxy/converter"
	"kiro-proxy/router"
	"kiro-proxy/store"
	"kiro-proxy/upstreamproxy"
	"log"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// requestUsageRecorder 只记录请求指标，不接触或保存请求、响应正文。
type requestUsageRecorder struct {
	db         *store.DB
	item       store.RequestUsage
	model      *store.Model
	start      time.Time
	firstByte  atomic.Int64
	itemMu     sync.Mutex
	traceMu    sync.Mutex
	trace      requestTraceMetrics
	errorMu    sync.Mutex
	readError  string
	contentMu  sync.Mutex
	content    *contentMetrics
	once       sync.Once
	autoReason string
}

func newRequestUsageRecorder(db *store.DB, start time.Time, model *store.Model, requestPath, protocol string) *requestUsageRecorder {
	if start.IsZero() {
		start = time.Now()
	}
	item := store.RequestUsage{RequestPath: requestPath, Protocol: protocol}
	if model != nil {
		item.ModelID = statsModelID(model)
		item.Provider = model.Provider
		item.Endpoint = model.Endpoint
		item.ReasoningEffort, _ = converter.NormalizeReasoningEffort(model.ReasoningEffort)
		item.ServiceTier = model.ServiceTier
	}
	return &requestUsageRecorder{db: db, item: item, model: model, start: start}
}

func (r *requestUsageRecorder) setModel(model *store.Model) {
	if r == nil || model == nil {
		return
	}
	r.itemMu.Lock()
	defer r.itemMu.Unlock()
	r.model = model
	r.item.ModelID = statsModelID(model)
	r.item.Provider = model.Provider
	r.item.Endpoint = model.Endpoint
	r.item.ServiceTier = model.ServiceTier
	r.item.SelectedModelConfigID = model.ID
	r.item.ReasoningEffort, _ = converter.NormalizeReasoningEffort(model.ReasoningEffort)
}

func (r *requestUsageRecorder) setRoute(route *router.Route) {
	if r == nil || route == nil {
		return
	}
	r.itemMu.Lock()
	defer r.itemMu.Unlock()
	r.item.RequestedModel = route.RequestedName
	r.autoReason = route.AutoReason
	if r.autoReason != "" {
		r.item.DispatchReason = "auto:" + r.autoReason
	}
	if route.Group != nil {
		r.item.RouteGroupID = route.Group.ID
	}
}

func (r *requestUsageRecorder) setFailoverCount(count int) {
	if r == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	r.itemMu.Lock()
	r.item.FailoverCount = count
	r.itemMu.Unlock()
}

func (r *requestUsageRecorder) setDispatchReason(reason string) {
	if r == nil {
		return
	}
	r.itemMu.Lock()
	reason = strings.TrimSpace(reason)
	if r.autoReason != "" {
		r.item.DispatchReason = "auto:" + r.autoReason
		if reason != "" {
			r.item.DispatchReason += ";" + reason
		}
	} else {
		r.item.DispatchReason = reason
	}
	r.itemMu.Unlock()
}

func (r *requestUsageRecorder) setFailoverReason(reason string) {
	if r == nil {
		return
	}
	r.itemMu.Lock()
	r.item.FailoverReason = strings.TrimSpace(reason)
	r.itemMu.Unlock()
}

func (r *requestUsageRecorder) addCapacityWait(duration time.Duration) {
	if r == nil || duration <= 0 {
		return
	}
	r.itemMu.Lock()
	r.item.CapacityWaitMS += duration.Milliseconds()
	r.itemMu.Unlock()
}

func (r *requestUsageRecorder) markFirstByte(at time.Time) {
	if r == nil || at.IsZero() {
		return
	}
	r.firstByte.CompareAndSwap(0, at.UnixNano())
}

func (r *requestUsageRecorder) setReasoningEffort(value string) {
	if r == nil {
		return
	}
	r.itemMu.Lock()
	r.item.ReasoningEffort = value
	r.itemMu.Unlock()
}

func (r *requestUsageRecorder) attemptStart() time.Time {
	if r == nil {
		return time.Time{}
	}
	r.traceMu.Lock()
	defer r.traceMu.Unlock()
	return r.trace.attemptStarted
}

func (r *requestUsageRecorder) contentOptions(protocol contentMetricProtocol, onFirstContent func(time.Time, string)) contentMetricOptions {
	return contentMetricOptions{protocol: protocol, start: r.start, onFirstContent: onFirstContent}
}

type requestTraceMetrics struct {
	attemptStarted         time.Time
	dnsStarted             time.Time
	connectStarted         time.Time
	tlsStarted             time.Time
	gotConnection          time.Time
	requestWritten         time.Time
	conversionMS           int64
	auditMS                int64
	sessionReadMS          int64
	authMS                 int64
	dnsMS                  int64
	connectMS              int64
	tlsMS                  int64
	requestSendMS          int64
	upstreamWaitMS         int64
	responseHeaderMS       int64
	retries                int
	reusedConnection       bool
	wireAttemptCount       int
	transportFallbackCount int
}

func (r *requestUsageRecorder) beginAttempt() {
	if r == nil {
		return
	}
	r.traceMu.Lock()
	if !r.trace.attemptStarted.IsZero() {
		r.trace.retries++
	}
	r.trace.attemptStarted = time.Now()
	r.trace.dnsStarted = time.Time{}
	r.trace.connectStarted = time.Time{}
	r.trace.tlsStarted = time.Time{}
	r.trace.gotConnection = time.Time{}
	r.trace.requestWritten = time.Time{}
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) setConversionDuration(started time.Time) {
	if r == nil {
		return
	}
	r.traceMu.Lock()
	r.trace.conversionMS += time.Since(started).Milliseconds()
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) setAuditDuration(started time.Time) {
	if r == nil || started.IsZero() {
		return
	}
	r.traceMu.Lock()
	r.trace.auditMS += time.Since(started).Milliseconds()
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) setSessionReadDuration(started time.Time) {
	if r == nil || started.IsZero() {
		return
	}
	r.traceMu.Lock()
	r.trace.sessionReadMS += time.Since(started).Milliseconds()
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) recordWireAttempt(fallback bool) {
	if r == nil {
		return
	}
	r.traceMu.Lock()
	r.trace.wireAttemptCount++
	if fallback {
		r.trace.transportFallbackCount++
	}
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) addAuthDuration(duration time.Duration) {
	if r == nil {
		return
	}
	r.traceMu.Lock()
	r.trace.authMS += duration.Milliseconds()
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) traceContext(ctx context.Context, attempt ...*upstreamAttemptState) context.Context {
	if r == nil {
		if len(attempt) > 0 && attempt[0] != nil {
			return withUpstreamOperation(ctx, attempt[0])
		}
		return ctx
	}
	var op *upstreamAttemptState
	if len(attempt) > 0 {
		op = attempt[0]
	}
	trace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			r.traceMu.Lock()
			r.trace.dnsStarted = time.Now()
			r.traceMu.Unlock()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			r.traceMu.Lock()
			if !r.trace.dnsStarted.IsZero() {
				r.trace.dnsMS += time.Since(r.trace.dnsStarted).Milliseconds()
			}
			r.traceMu.Unlock()
		},
		ConnectStart: func(_, _ string) {
			r.traceMu.Lock()
			r.trace.connectStarted = time.Now()
			r.traceMu.Unlock()
		},
		ConnectDone: func(_, _ string, _ error) {
			r.traceMu.Lock()
			if !r.trace.connectStarted.IsZero() {
				r.trace.connectMS += time.Since(r.trace.connectStarted).Milliseconds()
			}
			r.traceMu.Unlock()
		},
		TLSHandshakeStart: func() {
			r.traceMu.Lock()
			r.trace.tlsStarted = time.Now()
			r.traceMu.Unlock()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			r.traceMu.Lock()
			if !r.trace.tlsStarted.IsZero() {
				r.trace.tlsMS += time.Since(r.trace.tlsStarted).Milliseconds()
			}
			r.traceMu.Unlock()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			r.traceMu.Lock()
			r.trace.reusedConnection = info.Reused
			r.trace.gotConnection = time.Now()
			r.traceMu.Unlock()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if op != nil {
				if info.Err != nil {
					op.mark(upstreamSendWriteStarted)
				} else {
					op.mark(upstreamSendWritten)
				}
			}
			r.traceMu.Lock()
			r.trace.requestWritten = time.Now()
			started := r.trace.gotConnection
			if started.IsZero() {
				started = r.trace.attemptStarted
			}
			if !started.IsZero() {
				r.trace.requestSendMS += r.trace.requestWritten.Sub(started).Milliseconds()
			}
			r.traceMu.Unlock()
		},
		GotFirstResponseByte: func() {
			if op != nil {
				op.mark(upstreamResponseHeaders)
			}
			r.traceMu.Lock()
			r.trace.responseHeaderMS = time.Since(r.start).Milliseconds()
			if !r.trace.requestWritten.IsZero() {
				r.trace.upstreamWaitMS += time.Since(r.trace.requestWritten).Milliseconds()
			}
			r.traceMu.Unlock()
		},
	}
	ctx = httptrace.WithClientTrace(ctx, trace)
	return upstreamproxy.WithAttemptObserver(ctx, r.recordWireAttempt)
}

func (r *requestUsageRecorder) wrapResponse(resp *http.Response, options ...contentMetricOptions) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	if len(options) > 0 {
		metrics := newContentMetrics(options[0])
		r.contentMu.Lock()
		r.content = metrics
		r.contentMu.Unlock()
		resp.Body = metrics.wrap(resp.Body)
	}
	resp.Body = &usageObservedBody{ReadCloser: resp.Body, recorder: r}
}

func (r *requestUsageRecorder) wrapResponseForModel(resp *http.Response, model *store.Model, endpoint string, onFirstContent func(time.Time, string)) {
	if r == nil || resp == nil {
		return
	}
	provider := ""
	if model != nil {
		provider = model.Provider
		if endpoint == "" {
			endpoint = model.Endpoint
		}
	}
	protocol := contentMetricProtocolForResponse(resp, provider, endpoint)
	if protocol == "" {
		r.wrapResponse(resp)
		return
	}
	r.wrapResponse(resp, r.contentOptions(protocol, onFirstContent))
}

func (r *requestUsageRecorder) finish(status int, usage localCompatUsage, errorMessage string) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if errorMessage == "" {
			r.errorMu.Lock()
			errorMessage = r.readError
			r.errorMu.Unlock()
		}
		r.itemMu.Lock()
		defer r.itemMu.Unlock()
		r.item.StatusCode = status
		r.item.Success = status >= 200 && status < 400 && errorMessage == ""
		r.item.InputTokens = usage.InputTokens
		r.item.OutputTokens = usage.OutputTokens
		r.item.CacheReadTokens = usage.CacheReadTokens
		r.item.CacheWriteTokens = usage.CacheWriteTokens
		breakdown := store.CalculateCreditBreakdown(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, r.model)
		r.item.Credits = store.CalculateCredits(usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens, r.model)
		r.item.InputCost = breakdown.Input
		r.item.OutputCost = breakdown.Output
		r.item.CacheReadCost = breakdown.CacheRead
		r.item.CacheWriteCost = breakdown.CacheWrite
		r.item.DurationMS = time.Since(r.start).Milliseconds()
		if firstByte := r.firstByte.Load(); firstByte > 0 {
			r.item.FirstByteMS = time.Unix(0, firstByte).Sub(r.start).Milliseconds()
		}
		r.traceMu.Lock()
		r.item.ConversionMS = r.trace.conversionMS
		r.item.AuditMS = r.trace.auditMS
		r.item.SessionReadMS = r.trace.sessionReadMS
		r.item.AuthMS = r.trace.authMS
		r.item.DNSMS = r.trace.dnsMS
		r.item.ConnectMS = r.trace.connectMS
		r.item.TLSMS = r.trace.tlsMS
		r.item.RequestSendMS = r.trace.requestSendMS
		r.item.UpstreamWaitMS = r.trace.upstreamWaitMS
		r.item.ResponseHeaderMS = r.trace.responseHeaderMS
		r.item.RetryCount = r.trace.retries
		r.item.ReusedConnection = r.trace.reusedConnection
		r.item.WireAttemptCount = r.trace.wireAttemptCount
		r.item.TransportFallbackCount = r.trace.transportFallbackCount
		r.traceMu.Unlock()
		if r.item.FirstByteMS > r.item.ResponseHeaderMS {
			r.item.StreamWaitMS = r.item.FirstByteMS - r.item.ResponseHeaderMS
		} else if r.item.FirstByteMS == 0 && r.item.ResponseHeaderMS > 0 && r.item.DurationMS > r.item.ResponseHeaderMS {
			r.item.StreamWaitMS = r.item.DurationMS - r.item.ResponseHeaderMS
		}
		r.item.ErrorMessage = sanitizeUsageError(errorMessage)
		r.contentMu.Lock()
		content := r.content
		r.contentMu.Unlock()
		if content != nil {
			snapshot := content.snapshot()
			r.item.ContentMeasurementComplete = snapshot.measurementComplete
			r.item.ContentMeasured = snapshot.contentMeasured
			r.item.FirstEventMS = elapsedMetricMS(r.start, snapshot.firstEventAt)
			r.item.FirstContentMS = elapsedMetricMS(r.start, snapshot.firstContentAt)
			r.item.LastContentMS = elapsedMetricMS(r.start, snapshot.lastContentAt)
			r.item.FirstContentType = snapshot.firstContentType
			r.item.MaxContentGapMS = durationMetricMS(snapshot.maxContentGap)
		}
		if err := r.db.InsertRequestUsage(&r.item); err != nil {
			log.Printf("[request-usage] 写入失败: %v", err)
		}
	})
}

func elapsedMetricMS(start, at time.Time) int64 {
	if start.IsZero() || at.IsZero() || at.Before(start) {
		return 0
	}
	return durationMetricMS(at.Sub(start))
}

func durationMetricMS(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	value := duration.Milliseconds()
	if value < 1 {
		return 1
	}
	return value
}

type usageObservedBody struct {
	io.ReadCloser
	recorder *requestUsageRecorder
}

func (b *usageObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.recorder.firstByte.CompareAndSwap(0, time.Now().UnixNano())
	}
	if err != nil && err != io.EOF {
		b.recorder.errorMu.Lock()
		if b.recorder.readError == "" {
			b.recorder.readError = err.Error()
		}
		b.recorder.errorMu.Unlock()
	}
	return n, err
}

func sanitizeUsageError(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	// 仅保留异常概要的首行，避免上游错误将请求或返回内容带入统计表。
	if index := strings.IndexAny(message, "\r\n"); index >= 0 {
		message = message[:index]
	}
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
