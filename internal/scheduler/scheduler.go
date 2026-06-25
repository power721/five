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
	MarkShareShelved(ctx context.Context, shareCode, errText string) error
	DedupeShareTitles(ctx context.Context, dryRun bool) ([]model.ShareRename, error)
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
	gate     *PauseGate
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

// SetPauseGate attaches a pause switch. When set and paused, RunOnce short-
// circuits with errPaused (no share is crawled or marked failed) and RunLoop
// parks until the gate is resumed. Daemon-only; nil (the default) means the
// scheduler never pauses.
func (s *Scheduler) SetPauseGate(g *PauseGate) { s.gate = g }

func (s *Scheduler) paused() bool { return s.gate != nil && s.gate.Paused() }

// errPaused signals that a run was aborted because the pause gate is on. It is
// not a real error: RunLoop treats it as "park until resumed".
var errPaused = errors.New("scheduler paused")

// pausePollInterval is how often RunLoop re-checks the pause gate while parked.
// Package-level var so tests can shrink it.
var pausePollInterval = 500 * time.Millisecond

func (s *Scheduler) RunOnce(ctx context.Context, now int64) (bool, error) {
	shares, err := s.registry.ListSharesForCrawl(ctx, now)
	if err != nil {
		return false, err
	}
	proxyFailureOnly := len(shares) > 0
	for _, share := range shares {
		if s.paused() {
			return false, errPaused
		}
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
			if s.paused() {
				// The crawler bailed because the gate went on mid-share. Treat it
				// as a pause, not a failure — no RecordShareFailure, no shelve.
				return false, errPaused
			}
			if api115.IsEmptyDataError(err) && share.FailureCount+1 >= emptyDataGiveUpFailures {
				// "empty data with nonzero count" is retryable in case a cookie
				// refresh clears it, but for some shares 115 never returns data.
				// After a few tries, shelve the share so the scheduler stops
				// re-queuing it every backoff cycle and moves on to other shares.
				// We shelve (keep QUARANTINE + far-future retry) rather than mark
				// DEAD: these shares may still hold previously-crawled files, and
				// DEAD shares are pruned with their files at export time.
				if err := s.registry.MarkShareShelved(ctx, share.ShareCode, err.Error()); err != nil {
					return false, err
				}
				s.logger.Printf("event=share_crawl_finished share=%s result=shelved reason=empty_data_persistent failures=%d error=%q", share.ShareCode, share.FailureCount+1, err.Error())
				proxyFailureOnly = false
			} else {
				if err := s.registry.RecordShareFailure(ctx, share.ShareCode, err.Error()); err != nil {
					return false, err
				}
				s.logger.Printf("event=share_crawl_finished share=%s result=failure error=%q", share.ShareCode, err.Error())
				if !api115.IsProxyFailure(err) {
					proxyFailureOnly = false
				}
			}
		}
	}
	// Dedupe share titles after the crawl so newly added shares (and any titles
	// set this run) cannot collide. Cheap: an in-memory group-by that writes only
	// when collisions exist. Runs every cycle, so admin/import-added shares are
	// covered once crawled.
	renames, err := s.registry.DedupeShareTitles(ctx, false)
	if err != nil {
		return proxyFailureOnly, err
	}
	for _, r := range renames {
		s.logger.Printf("event=share_title_deduped share=%s from=%q to=%q", r.ShareCode, r.From, r.To)
	}
	return proxyFailureOnly, nil
}

func (s *Scheduler) RunLoop(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	proxyFailureRuns := 0

	for {
		if s.paused() {
			// Park until resumed or shut down. RunOnce is not called while paused,
			// so no share is crawled.
			if err := s.waitForResume(ctx); err != nil {
				return nil
			}
			continue
		}
		proxyFailureOnly, err := s.RunOnce(ctx, s.now().Unix())
		if errors.Is(err, errPaused) {
			// Pause hit mid-cycle; loop top parks until resume.
			continue
		}
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

// emptyDataGiveUpFailures caps how many times a share is retried for the
// "empty data with nonzero count" snap error before the scheduler abandons it
// (MarkShareDead). For some shares 115 never returns data even though count>0,
// so retrying forever — the old behavior — burned every backoff cycle on shares
// that would never succeed. Package-level var so tests can shrink it.
var emptyDataGiveUpFailures = 4

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

// waitForResume blocks while the gate is paused, returning ctx.Err() if the
// daemon shuts down first, or nil once the gate is resumed.
func (s *Scheduler) waitForResume(ctx context.Context) error {
	timer := time.NewTimer(pausePollInterval)
	defer timer.Stop()
	for s.paused() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			timer.Reset(pausePollInterval)
		}
	}
	return nil
}
