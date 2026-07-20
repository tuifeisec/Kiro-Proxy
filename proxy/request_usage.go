package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"kiro-proxy/store"
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
	db        *store.DB
	item      store.RequestUsage
	model     *store.Model
	start     time.Time
	firstByte atomic.Int64
	traceMu   sync.Mutex
	trace     requestTraceMetrics
	errorMu   sync.Mutex
	readError string
	once      sync.Once
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
		item.ServiceTier = model.ServiceTier
	}
	return &requestUsageRecorder{db: db, item: item, model: model, start: start}
}

type requestTraceMetrics struct {
	attemptStarted   time.Time
	dnsStarted       time.Time
	connectStarted   time.Time
	tlsStarted       time.Time
	gotConnection    time.Time
	requestWritten   time.Time
	conversionMS     int64
	authMS           int64
	dnsMS            int64
	connectMS        int64
	tlsMS            int64
	requestSendMS    int64
	upstreamWaitMS   int64
	responseHeaderMS int64
	retries          int
	reusedConnection bool
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

func (r *requestUsageRecorder) addAuthDuration(duration time.Duration) {
	if r == nil {
		return
	}
	r.traceMu.Lock()
	r.trace.authMS += duration.Milliseconds()
	r.traceMu.Unlock()
}

func (r *requestUsageRecorder) traceContext(ctx context.Context) context.Context {
	if r == nil {
		return ctx
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
		WroteRequest: func(httptrace.WroteRequestInfo) {
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
			r.traceMu.Lock()
			r.trace.responseHeaderMS = time.Since(r.start).Milliseconds()
			if !r.trace.requestWritten.IsZero() {
				r.trace.upstreamWaitMS += time.Since(r.trace.requestWritten).Milliseconds()
			}
			r.traceMu.Unlock()
		},
	}
	return httptrace.WithClientTrace(ctx, trace)
}

func (r *requestUsageRecorder) wrapResponse(resp *http.Response) {
	if r == nil || resp == nil || resp.Body == nil {
		return
	}
	resp.Body = &usageObservedBody{ReadCloser: resp.Body, recorder: r}
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
		r.item.AuthMS = r.trace.authMS
		r.item.DNSMS = r.trace.dnsMS
		r.item.ConnectMS = r.trace.connectMS
		r.item.TLSMS = r.trace.tlsMS
		r.item.RequestSendMS = r.trace.requestSendMS
		r.item.UpstreamWaitMS = r.trace.upstreamWaitMS
		r.item.ResponseHeaderMS = r.trace.responseHeaderMS
		r.item.RetryCount = r.trace.retries
		r.item.ReusedConnection = r.trace.reusedConnection
		r.traceMu.Unlock()
		if r.item.FirstByteMS > r.item.ResponseHeaderMS {
			r.item.StreamWaitMS = r.item.FirstByteMS - r.item.ResponseHeaderMS
		}
		r.item.ErrorMessage = sanitizeUsageError(errorMessage)
		if err := r.db.InsertRequestUsage(&r.item); err != nil {
			log.Printf("[request-usage] 写入失败: %v", err)
		}
	})
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
