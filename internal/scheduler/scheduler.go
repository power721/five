package scheduler

import (
	"context"
	"errors"
	"io"
	"log"
	"time"
)

import (
	"five/internal/api115"
	"five/internal/logutil"
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
	logger   *log.Logger
}

func New(registry Registry, runner Runner, logWriter io.Writer) *Scheduler {
	return &Scheduler{
		registry: registry,
		runner:   runner,
		logger:   logutil.New(logWriter),
	}
}

func (s *Scheduler) RunOnce(ctx context.Context, now int64) error {
	shares, err := s.registry.ListSharesForCrawl(ctx, now)
	if err != nil {
		return err
	}
	for _, share := range shares {
		s.logger.Printf("event=share_crawl_started share=%s", share.ShareCode)
		err := s.runner.CrawlShare(ctx, share, now)
		switch {
		case err == nil:
			if err := s.registry.MarkShareCrawled(ctx, share.ShareCode, now); err != nil {
				return err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=success", share.ShareCode)
		case errors.Is(err, api115.ErrDeadShare):
			if err := s.registry.MarkShareDead(ctx, share.ShareCode, err.Error()); err != nil {
				return err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=dead error=%q", share.ShareCode, err.Error())
		default:
			if err := s.registry.RecordShareFailure(ctx, share.ShareCode, err.Error()); err != nil {
				return err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=failure error=%q", share.ShareCode, err.Error())
			if api115.IsProxyFailure(err) {
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
