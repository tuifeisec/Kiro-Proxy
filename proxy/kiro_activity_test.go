package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kiro-proxy/store"

	"github.com/elazarl/goproxy"
)

const (
	kiroActivityMarker = "activity-body-sensitive-marker"
	kiroActivityAuth   = "activity-auth-sensitive-marker"
)

func newKiroActivityTestServer(t *testing.T, mode string, dialAddress string) (*Server, *store.DB, string) {
	t.Helper()
	oldMode := GetProxyMode()
	SetProxyMode(mode)
	t.Cleanup(func() { SetProxyMode(oldMode) })

	db, err := store.Open(filepath.Join(t.TempDir(), "activity-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	logPath := filepath.Join(t.TempDir(), "traffic.jsonl")
	logger, err := NewTrafficLogger(logPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)

	p := goproxy.NewProxyHttpServer()
	p.OnRequest().HandleConnect(goproxy.AlwaysMitm)
	if dialAddress != "" {
		p.Tr.Proxy = nil
		p.Tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, dialAddress)
		}
	}
	stats := store.NewStats(db)
	t.Cleanup(stats.Close)
	s := &Server{proxy: p, logger: logger, db: db, stats: stats}
	s.setupInterceptor()
	s.setupCaptureResponse()
	return s, db, logPath
}

func TestKiroActivitySuppressesForwardAndCapture(t *testing.T) {
	var officialCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		officialCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{ModeForward, ModeCapture} {
		t.Run(mode, func(t *testing.T) {
			s, db, logPath := newKiroActivityTestServer(t, mode, upstreamURL.Host)
			req := httptest.NewRequest(http.MethodPost, "http://RUNTIME.us-east-1.kiro.dev:443/agents/activity?source=test", strings.NewReader(kiroActivityMarker))
			req.Header.Set("Authorization", "Bearer "+kiroActivityAuth)
			rr := httptest.NewRecorder()
			s.proxy.ServeHTTP(rr, req)

			if rr.Code != http.StatusNoContent || rr.Body.Len() != 0 {
				t.Fatalf("activity response = %d %q, want empty 204", rr.Code, rr.Body.String())
			}
			if rr.Header().Get("Content-Length") != "0" && rr.Header().Get("Content-Length") != "" {
				t.Fatalf("unexpected Content-Length: %q", rr.Header().Get("Content-Length"))
			}
			if got := officialCalls.Load(); got != 0 {
				t.Fatalf("official mock calls = %d, want 0", got)
			}
			rows, _, err := db.QueryRequestUsage("", "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("model usage rows = %d, want 0", len(rows))
			}
			s.logger.Close()
			logs, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{kiroActivityMarker, kiroActivityAuth} {
				if strings.Contains(string(logs), marker) {
					t.Fatalf("activity log contains sensitive marker %q: %s", marker, logs)
				}
			}
		})
	}
}

func TestKiroActivityRequestBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		method string
		url    string
		host   string
		want   bool
	}{
		{name: "root host", method: http.MethodPost, url: "https://runtime.kiro.dev/agents/activity", want: true},
		{name: "regional host with port and query", method: http.MethodPost, url: "https://RUNTIME.us-east-1.kiro.dev:443/agents/activity?x=1", want: true},
		{name: "host fallback with port", method: http.MethodPost, url: "https:///agents/activity", host: "runtime.kiro.dev:443", want: true},
		{name: "wrong method", method: http.MethodPut, url: "https://runtime.kiro.dev/agents/activity", want: false},
		{name: "near path", method: http.MethodPost, url: "https://runtime.kiro.dev/agents/activity/", want: false},
		{name: "path suffix", method: http.MethodPost, url: "https://runtime.kiro.dev/agents/activity-extra", want: false},
		{name: "unrelated domain", method: http.MethodPost, url: "https://notruntime.kiro.dev/agents/activity", want: false},
		{name: "domain suffix attack", method: http.MethodPost, url: "https://runtime.kiro.dev.attacker.invalid/agents/activity", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = tc.host
			if got := isKiroActivityRequest(req); got != tc.want {
				t.Fatalf("isKiroActivityRequest = %v, want %v", got, tc.want)
			}
		})
	}
}

type kiroActivityFailingBody struct {
	readErr  error
	closeErr error
}

func (b *kiroActivityFailingBody) Read([]byte) (int, error) {
	if b.readErr == nil {
		return 0, io.EOF
	}
	return 0, b.readErr
}
func (b *kiroActivityFailingBody) Close() error { return b.closeErr }

type kiroActivityOneShotReadErrorBody struct {
	data         []byte
	readErr      error
	failed       bool
	failWithData bool
}

func (b *kiroActivityOneShotReadErrorBody) Read(p []byte) (int, error) {
	if !b.failed {
		b.failed = true
		if b.failWithData {
			n := copy(p, b.data)
			b.data = b.data[n:]
			return n, b.readErr
		}
		return 0, b.readErr
	}
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *kiroActivityOneShotReadErrorBody) Close() error { return nil }

func TestKiroActivityReadOrCloseFailureDoesNotSucceed(t *testing.T) {
	readErr := errors.New("synthetic body read failure")
	closeErr := errors.New("synthetic body close failure")
	s := &Server{}
	for _, tc := range []struct {
		name string
		body io.ReadCloser
		want error
	}{
		{name: "read failure", body: &kiroActivityFailingBody{readErr: readErr}, want: readErr},
		{name: "close failure", body: &kiroActivityFailingBody{closeErr: closeErr}, want: closeErr},
		{name: "both failures", body: &kiroActivityFailingBody{readErr: readErr, closeErr: closeErr}, want: errors.Join(readErr, closeErr)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
			req.Body = tc.body
			_, parseErr, err := s.discardKiroActivityRequestBody(req)
			if tc.name == "both failures" {
				if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
					t.Fatalf("joined discard error = %v, want both read and close errors", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("discard error = %v, want to contain %v", err, tc.want)
			}
			_ = parseErr
		})
	}

	server, _, _ := newKiroActivityTestServer(t, ModeForward, "")
	req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
	req.Body = &kiroActivityFailingBody{readErr: readErr}
	rr := httptest.NewRecorder()
	server.proxy.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("read failure response = %d, want 400", rr.Code)
	}
}

func TestKiroActivityParserPreservesTransientReadErrors(t *testing.T) {
	readErr := errors.New("transient activity body read failure")
	validJSON := kiroActivityRoutingPayload("sess_transient", "exec", "wf_transient", "n", "plan", `["wf_transient","plan"]`, "vibe")
	for _, tc := range []struct {
		name         string
		failWithData bool
	}{
		{name: "error before remaining body, drain later succeeds"},
		{name: "valid JSON bytes followed by read error", failWithData: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			body := &kiroActivityOneShotReadErrorBody{data: []byte(validJSON), readErr: readErr, failWithData: tc.failWithData}
			req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
			req.Body = body
			report, parseErr, drainErr := s.discardKiroActivityRequestBody(req)
			if !errors.Is(parseErr, readErr) || !errors.Is(drainErr, readErr) {
				t.Fatalf("parse/drain errors = %v / %v, want transient read error preserved", parseErr, drainErr)
			}
			if len(report.Entries) != 0 {
				t.Fatalf("read failure retained entries: %+v", report.Entries)
			}
			if _, ok := s.lookupKiroActivityRouting("sess_transient"); ok {
				t.Fatal("read failure committed activity state")
			}
		})
	}
}

func TestKiroActivitySyntaxFailurePreservesEarlyReadError(t *testing.T) {
	readErr := errors.New(kiroActivityMarker)
	sessionID := "sess_early_read_error"
	validJSON := kiroActivityRoutingPayload(sessionID, "exec", "wf_early", "n", "plan", `["wf_early","plan"]`, "vibe")
	newRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
		body := append([]byte("!"+kiroActivityMarker), validJSON...)
		req.Body = &kiroActivityOneShotReadErrorBody{data: body, readErr: readErr, failWithData: true}
		return req
	}

	server := &Server{}
	report, parseErr, drainErr := server.discardKiroActivityRequestBody(newRequest())
	if !errors.Is(parseErr, readErr) || !errors.Is(drainErr, readErr) {
		t.Fatalf("parse/drain errors = %v / %v, want original read error preserved", parseErr, drainErr)
	}
	if len(report.Entries) != 0 {
		t.Fatalf("syntax/read failure retained entries: %+v", report.Entries)
	}
	if _, ok := server.lookupKiroActivityRouting(sessionID); ok {
		t.Fatal("syntax/read failure committed activity state")
	}

	proxyServer, _, logPath := newKiroActivityTestServer(t, ModeForward, "")
	rr := httptest.NewRecorder()
	proxyServer.proxy.ServeHTTP(rr, newRequest())
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("early read failure response = %d, want 400", rr.Code)
	}
	proxyServer.logger.Close()
	logs, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), kiroActivityMarker) {
		t.Fatalf("activity error log contains body or reader error content: %s", logs)
	}
}

type kiroActivitySyntheticReader struct {
	remaining int64
	read      int64
}

func (r *kiroActivitySyntheticReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return int(n), nil
}
func (r *kiroActivitySyntheticReader) Close() error { return nil }

func TestKiroActivityDrainsLargeBodyAsStream(t *testing.T) {
	s, _, _ := newKiroActivityTestServer(t, ModeForward, "")
	body := &kiroActivitySyntheticReader{remaining: 40 << 20}
	req := httptest.NewRequest(http.MethodPost, "http://runtime.kiro.dev/agents/activity", nil)
	req.Body = body
	rr := httptest.NewRecorder()
	s.proxy.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent || body.read != 40<<20 {
		t.Fatalf("response=%d bytesRead=%d, want 204 and 40 MiB consumed", rr.Code, body.read)
	}
}

func TestKiroActivityDrainsRequestsAcrossConnectTunnel(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/ordinary" {
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read ordinary request body: %v", err)
		}
		if string(body) != "ordinary passthrough" {
			t.Errorf("ordinary request body = %q", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ordinary response")
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	s, _, _ := newKiroActivityTestServer(t, ModeForward, upstreamURL.Host)
	s.proxy.Tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // local test upstream only
	var connectCount atomic.Int64
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectCount.Add(1)
		}
		s.proxy.ServeHTTP(w, r)
	}))
	defer proxyServer.Close()

	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", proxyURL.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT runtime.kiro.dev:443 HTTP/1.1\r\nHost: runtime.kiro.dev:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	proxyReader := bufio.NewReader(conn)
	connectResponse, err := http.ReadResponse(proxyReader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	if connectResponse.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", connectResponse.StatusCode)
	}

	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: "runtime.kiro.dev"}) // local MITM certificate only
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("local MITM TLS handshake: %v", err)
	}
	responseReader := bufio.NewReader(tlsConn)

	sendRequest := func(req *http.Request) *http.Response {
		t.Helper()
		if err := req.Write(tlsConn); err != nil {
			t.Fatalf("write tunneled request: %v", err)
		}
		resp, err := http.ReadResponse(responseReader, req)
		if err != nil {
			t.Fatalf("read tunneled response: %v", err)
		}
		return resp
	}

	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodPost, "https://runtime.kiro.dev/agents/activity?seq="+fmt.Sprint(i), strings.NewReader(kiroActivityMarker))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+kiroActivityAuth)
		resp := sendRequest(req)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("activity request %d status = %d, want 204", i, resp.StatusCode)
		}
	}

	req, err := http.NewRequest(http.MethodPost, "https://runtime.kiro.dev/ordinary", strings.NewReader("ordinary passthrough"))
	if err != nil {
		t.Fatal(err)
	}
	resp := sendRequest(req)
	responseBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || string(responseBody) != "ordinary response" {
		t.Fatalf("ordinary response = %d %q", resp.StatusCode, responseBody)
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want only the ordinary passthrough", got)
	}
	if got := connectCount.Load(); got != 1 {
		t.Fatalf("CONNECT count = %d, want one reused TLS tunnel", got)
	}
}
