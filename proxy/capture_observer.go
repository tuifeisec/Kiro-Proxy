package proxy

import (
	"io"
	"sync"
)

type capturedResponse struct {
	Body       []byte
	Total      int64
	Completion string
	Err        error
}

type captureObserver struct {
	source     io.ReadCloser
	mu         sync.Mutex
	body       []byte
	total      int64
	limit      int
	completed  bool
	onComplete func(capturedResponse)
	closeOnce  sync.Once
	closeErr   error
}

func newCaptureObserver(source io.ReadCloser, limit int, complete func(capturedResponse)) *captureObserver {
	if limit < 0 {
		limit = 0
	}
	return &captureObserver{source: source, limit: limit, onComplete: complete}
}

func (r *captureObserver) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.mu.Lock()
	if !r.completed && n > 0 {
		r.total += int64(n)
		keep := min(n, r.limit-len(r.body))
		r.body = append(r.body, p[:keep]...)
	}
	r.mu.Unlock()
	if err != nil {
		if err == io.EOF {
			r.finish("eof", nil)
		} else {
			r.finish("error", err)
		}
	}
	return n, err
}

func (r *captureObserver) Close() error {
	r.closeOnce.Do(func() { r.closeErr = r.source.Close(); r.finish("closed", r.closeErr) })
	return r.closeErr
}

func (r *captureObserver) finish(completion string, err error) {
	r.mu.Lock()
	if r.completed {
		r.mu.Unlock()
		return
	}
	r.completed = true
	result := capturedResponse{Body: r.body, Total: r.total, Completion: completion, Err: err}
	r.body = nil
	complete := r.onComplete
	r.mu.Unlock()
	if complete != nil {
		complete(result)
	}
}
