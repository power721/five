package proxy

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type loopFetcher struct {
	calls *atomic.Int32
	now   time.Time
}

func (l loopFetcher) Fetch(context.Context) (Proxy, error) {
	l.calls.Add(1)
	return Proxy{
		ID:       "loop",
		URL:      "http://loop",
		State:    StateActive,
		Deadline: l.now.Add(3 * time.Minute),
	}, nil
}

func TestRunLoopStopsWithContext(t *testing.T) {
	now := time.Date(2026, 6, 18, 19, 10, 0, 0, time.UTC)
	mgr := New(Config{
		Now: func() time.Time { return now },
	})
	calls := &atomic.Int32{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunLoop(ctx, mgr, loopFetcher{calls: calls, now: now}, 1, 10*time.Millisecond)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run loop err: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("proxy loop did not stop on context cancel")
	}
	if calls.Load() == 0 {
		t.Fatal("expected proxy loop to fetch at least once")
	}
}
