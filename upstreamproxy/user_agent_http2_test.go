package upstreamproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"
)

// 验证生产 transport 的真实 TLS、ALPN 与 HTTP/2 编码，不只检查 RoundTripper 入参。
func TestClientHTTP2UserAgentReachesReceiver(t *testing.T) {
	type observation struct {
		protocol int
		ua       string
	}
	received := make(chan observation, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		received <- observation{r.ProtoMajor, r.UserAgent()}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client, err := NewClient("", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport := client.Transport.(*protocolFallbackTransport)
	// 只信任本测试接收端证书，生产 transport 的其他设置保持不变。
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, rt := range []http.RoundTripper{transport.primary, transport.fallback} {
		rt.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	}

	for _, tt := range []struct {
		name   string
		value  string
		setUA  bool
		wantUA string
		reused bool
	}{
		{"explicit", "OpenAI/Go 3.56.0", true, "OpenAI/Go 3.56.0", false},
		{"reused_connection", "OpenAI/Go 3.56.0", true, "OpenAI/Go 3.56.0", true},
		{"missing_header_control", "", false, "Go-http-client/2.0", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var mu sync.Mutex
			var writtenUA []string
			var reused bool
			ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) {
					mu.Lock()
					reused = info.Reused
					mu.Unlock()
				},
				WroteHeaderField: func(name string, values []string) {
					if strings.EqualFold(name, "User-Agent") {
						mu.Lock()
						writtenUA = append(writtenUA, values...)
						mu.Unlock()
					}
				},
			})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if tt.setUA {
				req.Header.Set("User-Agent", tt.value)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			select {
			case got := <-received:
				if got.protocol != 2 || got.ua != tt.wantUA {
					t.Fatalf("receiver protocol=%d UA=%q, want HTTP/2 UA=%q", got.protocol, got.ua, tt.wantUA)
				}
			case <-ctx.Done():
				t.Fatal("receiver did not observe request:", ctx.Err())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(writtenUA) != 1 || writtenUA[0] != tt.wantUA {
				t.Fatalf("encoded User-Agent=%q, want [%q]", writtenUA, tt.wantUA)
			}
			if reused != tt.reused {
				t.Fatalf("connection reused=%v, want %v", reused, tt.reused)
			}
		})
	}
}
