package main

import (
	"context"
	"fmt"

	"five/internal/model"
)

// crawlRegistrar is the subset of *store.Store that -mode crawl touches to
// register a share and record a finished crawl.
type crawlRegistrar interface {
	UpsertShare(ctx context.Context, share model.Share) error
	MarkShareCrawled(ctx context.Context, shareCode string, crawledAt int64) error
}

// crawlRunner runs one share's full BFS crawl. *crawler.Crawler satisfies it.
type crawlRunner interface {
	CrawlShare(ctx context.Context, share model.Share, crawledAt int64) error
}

// runCrawlOnce is the -mode crawl driver: it registers the share as ACTIVE
// (idempotent — an existing COMPLETED share flips back to ACTIVE for a forced
// re-crawl), runs the full crawl, and on success marks it COMPLETED. This is
// the same crawl→complete transition the daemon scheduler applies.
func runCrawlOnce(ctx context.Context, reg crawlRegistrar, runner crawlRunner, shareCode, receiveCode string, now int64) error {
	share := model.Share{ShareCode: shareCode, ReceiveCode: receiveCode, Status: "ACTIVE"}
	if err := reg.UpsertShare(ctx, share); err != nil {
		return fmt.Errorf("upsert share: %w", err)
	}
	if err := runner.CrawlShare(ctx, share, now); err != nil {
		return err
	}
	if err := reg.MarkShareCrawled(ctx, shareCode, now); err != nil {
		return fmt.Errorf("mark share crawled: %w", err)
	}
	return nil
}
