package proxy

import (
	"bytes"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

func TestCaptureObserverStreamsBeforeEOFAndBoundsLog(t *testing.T) {
	source, writer := io.Pipe()
	completed := make(chan capturedResponse, 1)
	body := newCaptureObserver(source, 4, func(r capturedResponse) { completed <- r })
	first := make(chan []byte, 1)
	go func() { p := make([]byte, 3); n, _ := body.Read(p); first <- p[:n] }()
	if _, err := writer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-first:
		if string(got) != "abc" {
			t.Fatalf("first=%q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first read waited for EOF")
	}
	select {
	case <-completed:
		t.Fatal("premature completion")
	default:
	}
	go func() { _, _ = writer.Write([]byte("defgh")); _ = writer.Close() }()
	rest, err := io.ReadAll(body)
	if err != nil || string(rest) != "defgh" {
		t.Fatalf("body=%q err=%v", rest, err)
	}
	result := <-completed
	if string(result.Body) != "abcd" || result.Total != 8 || result.Completion != "eof" {
		t.Fatalf("result=%+v", result)
	}
	_ = body.Close()
	select {
	case <-completed:
		t.Fatal("duplicate completion")
	default:
	}
}

type errorCaptureSource struct {
	closes int
	err    error
}

func (s *errorCaptureSource) Read(p []byte) (int, error) { return copy(p, "payload"), s.err }
func (s *errorCaptureSource) Close() error               { s.closes++; return nil }

func TestCaptureObserverPreservesReadErrorAndBytes(t *testing.T) {
	expected := errors.New("original read failure")
	source := &errorCaptureSource{err: expected}
	var result capturedResponse
	calls := 0
	body := newCaptureObserver(source, 32, func(r capturedResponse) { result = r; calls++ })
	p := make([]byte, 32)
	n, err := body.Read(p)
	if !errors.Is(err, expected) || !bytes.Equal(p[:n], []byte("payload")) {
		t.Fatalf("bytes=%q err=%v", p[:n], err)
	}
	_ = body.Close()
	_ = body.Close()
	if calls != 1 || source.closes != 1 || result.Err != expected || result.Completion != "error" {
		t.Fatalf("result=%+v calls=%d closes=%d", result, calls, source.closes)
	}
}

func TestCaptureObserverCloseUnblocksReadAndFinishesOnce(t *testing.T) {
	source, writer := io.Pipe()
	defer writer.Close()
	var calls atomic.Int32
	body := newCaptureObserver(source, 3, func(r capturedResponse) { calls.Add(1) })
	done := make(chan struct{})
	go func() { defer close(done); _, _ = body.Read(make([]byte, 8)) }()
	_ = body.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not release reader")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
