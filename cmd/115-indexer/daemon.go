package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type loopFunc func(context.Context) error

func runDaemon(ctx context.Context, loops ...loopFunc) error {
	if len(loops) == 0 {
		<-ctx.Done()
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, len(loops))
	var wg sync.WaitGroup
	for _, loop := range loops {
		wg.Add(1)
		go func(loop loopFunc) {
			defer wg.Done()
			errCh <- loop(ctx)
		}(loop)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	var firstErr error
	for i := 0; i < len(loops); i++ {
		err := <-errCh
		if err != nil && !errors.Is(err, context.Canceled) && firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	<-done
	if firstErr != nil {
		return firstErr
	}
	return nil
}

func pollInterval(v time.Duration, fallback time.Duration) time.Duration {
	if v <= 0 {
		return fallback
	}
	return v
}
