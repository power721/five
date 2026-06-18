package proxy

import (
	"context"
	"time"
)

func RunLoop(ctx context.Context, mgr *Manager, provider Fetcher, minAvailable int, interval time.Duration) error {
	return RunLoopWithValidator(ctx, mgr, provider, nil, minAvailable, interval)
}

func RunLoopWithValidator(ctx context.Context, mgr *Manager, provider Fetcher, validator Validator, minAvailable int, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := mgr.EnsureCapacity(ctx, provider, validator, minAvailable); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
