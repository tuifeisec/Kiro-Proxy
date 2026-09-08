package upstreamproxy

import "context"

type attemptObserverKey struct{}

// WithAttemptObserver 观测实际 RoundTrip 次数；fallback 只标识本次调用内的协议回退。
// 观察器必须并发安全，不修改请求，也不决定是否重试。
func WithAttemptObserver(ctx context.Context, observer func(fallback bool)) context.Context {
	return context.WithValue(ctx, attemptObserverKey{}, observer)
}

func observeAttempt(ctx context.Context, fallback bool) {
	if observer, ok := ctx.Value(attemptObserverKey{}).(func(bool)); ok && observer != nil {
		observer(fallback)
	}
}
