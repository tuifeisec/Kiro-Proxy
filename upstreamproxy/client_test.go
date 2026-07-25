package upstreamproxy

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandshakeFallbackReplaysBody(t *testing.T) {
	primaryCalls := 0
	fallbackCalls := 0
	transport := &handshakeFallbackTransport{
		primary: roundTripFunc(func(*http.Request) (*http.Response, error) {
			primaryCalls++
			return nil, errors.New("net/http: TLS handshake timeout")
		}),
		fallback: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			fallbackCalls++
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
	transport := &handshakeFallbackTransport{
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
