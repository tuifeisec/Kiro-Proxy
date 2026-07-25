package upstreamproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const SettingKey = "upstream_proxy_url"

func NewClient(proxyAddr string, timeout time.Duration) (*http.Client, error) {
	proxyURL, err := ParseProxyURL(proxyAddr)
	if err != nil {
		return nil, err
	}

	http2Transport := newTransport(proxyURL, true)
	http1Transport := newTransport(proxyURL, false)

	return &http.Client{
		Timeout: timeout,
		Transport: &handshakeFallbackTransport{
			primary:  http2Transport,
			fallback: http1Transport,
		},
	}, nil
}

func newTransport(proxyURL *url.URL, forceHTTP2 bool) *http.Transport {
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 32,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     5 * time.Minute,
		ForceAttemptHTTP2:   forceHTTP2,
		WriteBufferSize:     32 * 1024,
		ReadBufferSize:      32 * 1024,
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
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return transport
}

type handshakeFallbackTransport struct {
	primary  http.RoundTripper
	fallback http.RoundTripper
}

func (t *handshakeFallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.primary.RoundTrip(req)
	if err == nil || !isTLSHandshakeTimeout(err) || req.GetBody == nil || req.Context().Err() != nil {
		return resp, err
	}

	body, bodyErr := req.GetBody()
	if bodyErr != nil {
		return nil, fmt.Errorf("TLS handshake failed; HTTP/1.1 fallback body reset failed: %w", bodyErr)
	}
	fallbackReq := req.Clone(req.Context())
	fallbackReq.Body = body
	return t.fallback.RoundTrip(fallbackReq)
}

func (t *handshakeFallbackTransport) CloseIdleConnections() {
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
