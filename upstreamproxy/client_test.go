package upstreamproxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

type readErrorBody struct {
	err error
}

func (b *readErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b *readErrorBody) Close() error             { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandshakeFallbackReplaysBody(t *testing.T) {
	wantUserAgent := "OpenAI/Go 3.56.0"
	primaryCalls := 0
	fallbackCalls := 0
	transport := &protocolFallbackTransport{
		primary: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			primaryCalls++
			if got := req.UserAgent(); got != wantUserAgent {
				t.Errorf("primary User-Agent = %q, want %q", got, wantUserAgent)
			}
			return nil, errors.New("net/http: TLS handshake timeout")
		}),
		fallback: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			fallbackCalls++
			if got := req.UserAgent(); got != wantUserAgent {
				t.Errorf("fallback User-Agent = %q, want %q", got, wantUserAgent)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if string(body) != "payload" {
				return nil, errors.New("fallback body was not replayed")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewBufferString("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", wantUserAgent)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls = primary %d, fallback %d; want 1/1", primaryCalls, fallbackCalls)
	}
}

func TestHandshakeFallbackIgnoresOtherErrors(t *testing.T) {
	fallbackCalls := 0
	wantErr := errors.New("dial failed")
	transport := &protocolFallbackTransport{
		primary: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, wantErr
		}),
		fallback: roundTripFunc(func(*http.Request) (*http.Response, error) {
			fallbackCalls++
			return nil, nil
		}),
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewBufferString("payload"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(req)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v; want %v", err, wantErr)
	}
	if fallbackCalls != 0 {
		t.Fatalf("fallback calls = %d; want 0", fallbackCalls)
	}
}

func TestHTTP2BodyFailureUsesHTTP1ForNextRequest(t *testing.T) {
	primaryCalls := 0
	fallbackCalls := 0
	transport := &protocolFallbackTransport{
		primary: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			primaryCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				ProtoMajor: 2,
				Body:       &readErrorBody{err: errors.New("http2: response body closed")},
				Request:    req,
			}, nil
		}),
		fallback: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			fallbackCalls++
			return &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, Body: http.NoBody, Request: req}, nil
		}),
	}

	first, err := http.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewBufferString("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); !IsHTTP2Failure(err) {
		t.Fatalf("body error = %v, want HTTP/2 failure", err)
	}

	second, err := http.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewBufferString("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err = transport.RoundTrip(second)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls = primary %d, fallback %d; want 1/1", primaryCalls, fallbackCalls)
	}
}

func TestHTTP2RoundTripFailureReplaysOnHTTP1(t *testing.T) {
	wantUserAgent := "OpenAI/Go 3.56.0"
	primaryCalls := 0
	fallbackCalls := 0
	transport := &protocolFallbackTransport{
		primary: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			primaryCalls++
			if got := req.UserAgent(); got != wantUserAgent {
				t.Errorf("primary User-Agent = %q, want %q", got, wantUserAgent)
			}
			return nil, errors.New("http2: client connection lost")
		}),
		fallback: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			fallbackCalls++
			if got := req.UserAgent(); got != wantUserAgent {
				t.Errorf("fallback User-Agent = %q, want %q", got, wantUserAgent)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil || string(body) != "payload" {
				return nil, errors.New("fallback request body mismatch")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: req}, nil
		}),
	}
	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/responses", bytes.NewBufferString("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", wantUserAgent)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if primaryCalls != 1 || fallbackCalls != 1 {
		t.Fatalf("calls = primary %d, fallback %d; want 1/1", primaryCalls, fallbackCalls)
	}
}

func TestStreamingTransportHasLongHeaderTimeoutAndHTTP2Keepalive(t *testing.T) {
	transport := newTransport(nil, true)
	if transport.ResponseHeaderTimeout <= time.Minute {
		t.Fatalf("response header timeout = %v, want more than one minute", transport.ResponseHeaderTimeout)
	}
	if transport.HTTP2 == nil || transport.HTTP2.SendPingTimeout <= 0 || transport.HTTP2.PingTimeout <= 0 {
		t.Fatalf("HTTP/2 keepalive is not configured: %+v", transport.HTTP2)
	}
	fallback := newTransport(nil, false)
	if fallback.Protocols == nil || !fallback.Protocols.HTTP1() || fallback.Protocols.HTTP2() {
		t.Fatalf("fallback protocols = %v, want HTTP/1 only", fallback.Protocols)
	}
}
