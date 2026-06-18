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
