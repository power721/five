package main

import (
	"context"
	"errors"
	"time"
)

// shutdownGrace bounds how long runDaemon waits for loops to finish after
// shutdown is requested. Package-level var (not const) so tests can shrink it.
var shutdownGrace = 10 * time.Second

type loopFunc func(context.Context) error

func runDaemon(ctx context.Context, loops ...loopFunc) error {
	if len(loops) == 0 {
		<-ctx.Done()
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(loops))
	for _, loop := range loops {
		go func(loop loopFunc) {
			errCh <- loop(ctx)
		}(loop)
	}

	// Drain loop results. Once shutdown is requested (ctx cancelled by signal,
	// or a loop returned a real error that triggers cancel), bound the wait by
	// shutdownGrace so a stuck loop can't pin the process past systemd's
	// TimeoutStopSec. Loops still running when the grace expires are abandoned;
	// they (and any in-flight I/O) die with the process on exit.
	received := 0
	var firstErr error
	shutdown := ctx.Done()
	var deadline *time.Timer
	defer func() {
		if deadline != nil {
			deadline.Stop()
		}
	}()
	for received < len(loops) {
		var deadlineCh <-chan time.Time
		if deadline != nil {
			deadlineCh = deadline.C
		}
		select {
		case err := <-errCh:
			received++
			if err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
				firstErr = err
				cancel()
			}
		case <-shutdown:
			// Shutdown requested: arm the grace timer and stop selecting on
			// ctx.Done (a nil channel never fires) so we don't busy-loop.
			shutdown = nil
			deadline = time.NewTimer(shutdownGrace)
		case <-deadlineCh:
			// Grace exhausted with loops still running — exit anyway.
			return firstErr
		}
	}
	return firstErr
}

func pollInterval(v time.Duration, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}
