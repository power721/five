package scheduler

import (
	"context"
	"errors"
	"time"
)

import (
	"five/internal/api115"
	"five/internal/model"
)

type Registry interface {
	ListSharesForCrawl(ctx context.Context, now int64) ([]model.Share, error)
	MarkShareCrawled(ctx context.Context, shareCode string, crawledAt int64) error
	RecordShareFailure(ctx context.Context, shareCode, errText string) error
	MarkShareDead(ctx context.Context, shareCode, errText string) error
}

type Runner interface {
	CrawlShare(ctx context.Context, share model.Share, crawledAt int64) error
}

type Scheduler struct {
	registry Registry
	runner   Runner
}

func New(registry Registry, runner Runner) *Scheduler {
	return &Scheduler{registry: registry, runner: runner}
}

func (s *Scheduler) RunOnce(ctx context.Context, now int64) error {
	shares, err := s.registry.ListSharesForCrawl(ctx, now)
	if err != nil {
		return err
	}
	for _, share := range shares {
		err := s.runner.CrawlShare(ctx, share, now)
		switch {
		case err == nil:
			if err := s.registry.MarkShareCrawled(ctx, share.ShareCode, now); err != nil {
				return err
			}
		case errors.Is(err, api115.ErrDeadShare):
			if err := s.registry.MarkShareDead(ctx, share.ShareCode, err.Error()); err != nil {
				return err
			}
		default:
			if err := s.registry.RecordShareFailure(ctx, share.ShareCode, err.Error()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Scheduler) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := s.RunOnce(ctx, time.Now().Unix()); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
