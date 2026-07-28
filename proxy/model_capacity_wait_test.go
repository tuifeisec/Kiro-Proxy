package proxy

import (
	"context"
	"testing"
	"time"
)

func TestModelLoadTrackerAcquireWithWaitWakesOnRelease(t *testing.T) {
	tracker := newModelLoadTracker()
	first, acquired := tracker.acquire("model-a", 1)
	if !acquired {
		t.Fatal("initial capacity acquisition failed")
	}

	type result struct {
		lease    *modelLease
		acquired bool
	}
	resultCh := make(chan result, 1)
	go func() {
		lease, ok := tracker.acquireWithWait(context.Background(), "model-a", 1, time.Second)
		resultCh <- result{lease: lease, acquired: ok}
	}()

	time.Sleep(20 * time.Millisecond)
	first.release()

	select {
	case got := <-resultCh:
		if !got.acquired || got.lease == nil {
			t.Fatal("waiting acquisition was not woken by capacity release")
		}
		got.lease.release()
	case <-time.After(500 * time.Millisecond):
		t.Fatal("waiting acquisition did not wake promptly")
	}

	if active := tracker.snapshot()["model-a"]; active != 0 {
		t.Fatalf("active count after release = %d, want 0", active)
	}
}

func TestModelLoadTrackerAcquireWithWaitTimesOut(t *testing.T) {
	tracker := newModelLoadTracker()
	lease, acquired := tracker.acquire("model-a", 1)
	if !acquired {
		t.Fatal("initial capacity acquisition failed")
	}
	defer lease.release()

	started := time.Now()
	waitingLease, waitingAcquired := tracker.acquireWithWait(context.Background(), "model-a", 1, 30*time.Millisecond)
	if waitingAcquired || waitingLease != nil {
		t.Fatal("capacity acquisition must fail when the wait duration expires")
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Fatalf("unexpected wait duration: %v", elapsed)
	}
}

func TestModelLoadTrackerAcquireWithWaitHonorsContext(t *testing.T) {
	tracker := newModelLoadTracker()
	lease, acquired := tracker.acquire("model-a", 1)
	if !acquired {
		t.Fatal("initial capacity acquisition failed")
	}
	defer lease.release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	waitingLease, waitingAcquired := tracker.acquireWithWait(ctx, "model-a", 1, time.Second)
	if waitingAcquired || waitingLease != nil {
		t.Fatal("capacity acquisition must stop when its context is canceled")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("context cancellation was not observed promptly: %v", elapsed)
	}
}

func TestModelLoadTrackerAcquireWithWaitPreservesImmediateBehavior(t *testing.T) {
	tracker := newModelLoadTracker()
	unlimited, acquired := tracker.acquireWithWait(context.Background(), "unlimited", 0, time.Second)
	if !acquired || unlimited == nil {
		t.Fatal("unlimited model must acquire immediately")
	}
	unlimited.release()

	first, acquired := tracker.acquire("limited", 1)
	if !acquired {
		t.Fatal("initial limited capacity acquisition failed")
	}
	defer first.release()

	started := time.Now()
	waitingLease, waitingAcquired := tracker.acquireWithWait(context.Background(), "limited", 1, 0)
	if waitingAcquired || waitingLease != nil {
		t.Fatal("zero wait duration must preserve non-blocking acquisition")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("zero wait duration blocked unexpectedly: %v", elapsed)
	}
}
