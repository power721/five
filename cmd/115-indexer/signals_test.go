package main

import (
	"context"
	"testing"
	"time"
)

func TestContextWithSignalsCancelsParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalCtx, stop := contextWithSignals(ctx)
	defer stop()

	cancel()

	select {
	case <-signalCtx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("signal context did not cancel with parent context")
	}
}

func TestLocalOnlyListenAddr(t *testing.T) {
	if got := localOnlyListenAddr(":8080"); got != "127.0.0.1:8080" {
		t.Fatalf("localOnlyListenAddr(:8080) = %q", got)
	}
	if got := localOnlyListenAddr("127.0.0.1:8080"); got != "127.0.0.1:8080" {
		t.Fatalf("localOnlyListenAddr(127.0.0.1:8080) = %q", got)
	}
	if got := localOnlyListenAddr("localhost:8080"); got != "localhost:8080" {
		t.Fatalf("localOnlyListenAddr(localhost:8080) = %q", got)
	}
}
