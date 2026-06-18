package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunDaemonStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := &atomic.Int32{}

	loop := func(ctx context.Context) error {
		calls.Add(1)
		<-ctx.Done()
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- runDaemon(ctx, loop, loop)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDaemon returned err: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runDaemon did not stop on context cancel")
	}
	if calls.Load() != 2 {
		t.Fatalf("loop calls = %d, want 2", calls.Load())
	}
}

func TestRunDaemonCancelsPeersOnError(t *testing.T) {
	ctx := context.Background()
	stopped := make(chan struct{}, 1)

	errLoop := func(context.Context) error {
		return errors.New("boom")
	}
	waitLoop := func(ctx context.Context) error {
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	}

	err := runDaemon(ctx, errLoop, waitLoop)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("runDaemon err = %v, want boom", err)
	}

	select {
	case <-stopped:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("peer loop was not cancelled after error")
	}
}
