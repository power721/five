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
	sleep    func(context.Context, time.Duration) error
	now      func() time.Time
}

func New(registry Registry, runner Runner, logWriter io.Writer) *Scheduler {
	return &Scheduler{
		registry: registry,
		runner:   runner,
		logger:   logutil.New(logWriter),
		sleep:    sleepWithContext,
		now:      time.Now,
	}
}

func (s *Scheduler) RunOnce(ctx context.Context, now int64) (bool, error) {
	shares, err := s.registry.ListSharesForCrawl(ctx, now)
	if err != nil {
		return false, err
	}
	proxyFailureOnly := len(shares) > 0
	for _, share := range shares {
		s.logger.Printf("event=share_crawl_started share=%s", share.ShareCode)
		err := s.runner.CrawlShare(ctx, share, now)
		switch {
		case err == nil:
			proxyFailureOnly = false
			if err := s.registry.MarkShareCrawled(ctx, share.ShareCode, now); err != nil {
				return false, err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=success", share.ShareCode)
		case errors.Is(err, api115.ErrDeadShare):
			proxyFailureOnly = false
			if err := s.registry.MarkShareDead(ctx, share.ShareCode, err.Error()); err != nil {
				return false, err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=dead error=%q", share.ShareCode, err.Error())
		default:
			if err := s.registry.RecordShareFailure(ctx, share.ShareCode, err.Error()); err != nil {
				return false, err
			}
			s.logger.Printf("event=share_crawl_finished share=%s result=failure error=%q", share.ShareCode, err.Error())
			if !api115.IsProxyFailure(err) {
				proxyFailureOnly = false
			}
		}
	}
	return proxyFailureOnly, nil
}

func (s *Scheduler) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	proxyFailureRuns := 0

	for {
		proxyFailureOnly, err := s.RunOnce(ctx, s.now().Unix())
		if err != nil {
			return err
		}
		sleepFor := interval
		if proxyFailureOnly {
			proxyFailureRuns++
			sleepFor = proxyBackoff(proxyFailureRuns)
			s.logger.Printf("event=scheduler_proxy_backoff consecutive_failures=%d sleep=%s", proxyFailureRuns, sleepFor)
		} else {
			proxyFailureRuns = 0
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if err := s.sleep(ctx, sleepFor); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
	}
}

func proxyBackoff(consecutiveFailures int) time.Duration {
	switch {
	case consecutiveFailures <= 1:
		return 5 * time.Minute
	case consecutiveFailures == 2:
		return 15 * time.Minute
	case consecutiveFailures == 3:
		return 30 * time.Minute
	default:
		return time.Hour
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
