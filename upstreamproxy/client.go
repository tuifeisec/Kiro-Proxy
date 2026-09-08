package upstreamproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const SettingKey = "upstream_proxy_url"

const (
	responseHeaderTimeout = 5 * time.Minute
	http1FallbackTTL      = 10 * time.Minute
	http2SendPingTimeout  = 30 * time.Second
	http2PingTimeout      = 15 * time.Second
)

func NewClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	proxyURL, err := ParseProxyURL(proxyAddr)
	if err != nil {
		return nil, err
	}

	http2Transport := newTransport(proxyURL, true)
	http1Transport := newTransport(proxyURL, false)

	return &http.Client{
		Timeout: timeout,
		Transport: &protocolFallbackTransport{
			primary:  http2Transport,
			fallback: http1Transport,
		},
	}, nil
}

func newTransport(proxyURL *url.URL, forceHTTP2 bool) *http.Transport {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(forceHTTP2)
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 32,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     5 * time.Minute,
		ForceAttemptHTTP2:   forceHTTP2,
		WriteBufferSize:     32 * 1024,
		ReadBufferSize:      32 * 1024,
		Protocols:           protocols,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			conn, err := d.DialContext(ctx, "tcp4", addr)
			if err != nil {
				return nil, err
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetNoDelay(true)
			}
			return conn, nil
		},
		TLSHandshakeTimeout: 10 * time.Second,
		// 大上下文 Responses 请求的模型处理时间可能超过 20 秒；
		// 只限制响应头等待，不影响后续流式响应的持续时间。
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if forceHTTP2 {
		transport.HTTP2 = &http.HTTP2Config{
			SendPingTimeout: http2SendPingTimeout,
			PingTimeout:     http2PingTimeout,
		}
	}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return transport
}

type protocolFallbackTransport struct {
	primary  http.RoundTripper
	fallback http.RoundTripper

	mu                   sync.Mutex
	http1UntilByUpstream map[string]time.Time
}

func (t *protocolFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.preferHTTP1(req, time.Now()) {
		observeAttempt(req.Context(), false)
		return t.fallback.RoundTrip(req)
	}

	observeAttempt(req.Context(), false)
	resp, err := t.primary.RoundTrip(req)
	if err == nil {
		return t.observeHTTP2Response(req, resp), nil
	}
	if (!isTLSHandshakeTimeout(err) && !IsHTTP2Failure(err)) || req.GetBody == nil || req.Context().Err() != nil {
		return resp, err
	}
	t.markHTTP1(req, time.Now())

	body, bodyErr := req.GetBody()
	if bodyErr != nil {
		return nil, fmt.Errorf("HTTP/1.1 fallback body reset failed: %w", bodyErr)
	}
	fallbackReq := req.Clone(req.Context())
	fallbackReq.Body = body
	observeAttempt(fallbackReq.Context(), true)
	return t.fallback.RoundTrip(fallbackReq)
}

func (t *protocolFallbackTransport) observeHTTP2Response(req *http.Request, resp *http.Response) *http.Response {
	if resp == nil || resp.Body == nil || resp.ProtoMajor != 2 {
		return resp
	}
	resp.Body = &http2ObservedBody{
		ReadCloser: resp.Body,
		onFailure: func() {
			t.markHTTP1(req, time.Now())
		},
	}
	return resp
}

func (t *protocolFallbackTransport) preferHTTP1(req *http.Request, now time.Time) bool {
	key := upstreamKey(req)
	if key == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	until := t.http1UntilByUpstream[key]
	if until.IsZero() {
		return false
	}
	if !now.Before(until) {
		delete(t.http1UntilByUpstream, key)
		return false
	}
	return true
}

func (t *protocolFallbackTransport) markHTTP1(req *http.Request, now time.Time) {
	key := upstreamKey(req)
	if key == "" {
		return
	}
	t.mu.Lock()
	if t.http1UntilByUpstream == nil {
		t.http1UntilByUpstream = make(map[string]time.Time)
	}
	t.http1UntilByUpstream[key] = now.Add(http1FallbackTTL)
	t.mu.Unlock()
	log.Printf("[upstreamproxy] switching %s to HTTP/1.1 for %v after an HTTP/2 or TLS transport failure", key, http1FallbackTTL)
	if closer, ok := t.primary.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func upstreamKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return strings.ToLower(req.URL.Scheme + "://" + req.URL.Host)
}

type http2ObservedBody struct {
	io.ReadCloser
	onFailure func()
	once      sync.Once
}

func (b *http2ObservedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if IsHTTP2Failure(err) {
		b.once.Do(b.onFailure)
	}
	return n, err
}

func (t *protocolFallbackTransport) CloseIdleConnections() {
	if closer, ok := t.primary.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
	if closer, ok := t.fallback.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func isTLSHandshakeTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "TLS handshake timeout")
}

// IsHTTP2Failure reports errors that are specific to a broken HTTP/2 stream
// or connection and are therefore suitable for an HTTP/1.1 replay before
// downstream response commitment.
func IsHTTP2Failure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
		return false
	}
	message := strings.ToLower(err.Error())
	markers := []string{
		"http2:",
		"http2 ",
		"response body closed",
		"received goaway",
		"stream error:",
	}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func ParseProxyURL(proxyAddr string) (*url.URL, error) {
	proxyAddr = strings.TrimSpace(proxyAddr)
	if proxyAddr == "" {
		return nil, nil
	}
	if !strings.Contains(proxyAddr, "://") {
		proxyAddr = "http://" + proxyAddr
	}
	proxyURL, err := url.Parse(proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("代理地址格式错误: %w", err)
	}
	if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" && proxyURL.Scheme != "socks5" {
		return nil, fmt.Errorf("代理地址仅支持 http、https 或 socks5")
	}
	if proxyURL.Host == "" {
		return nil, fmt.Errorf("代理地址缺少 host")
	}
	return proxyURL, nil
}
