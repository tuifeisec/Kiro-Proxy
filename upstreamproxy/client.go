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
	transport := &http.Transport{
		DisableCompression:  true,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 32,
		MaxConnsPerHost:     0,
		IdleConnTimeout:     5 * time.Minute,
		ForceAttemptHTTP2:   true,
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
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	proxyURL, err := ParseProxyURL(proxyAddr)
	if err != nil {
		return nil, err
	}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	return &http.Client{Timeout: timeout, Transport: transport}, nil
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
