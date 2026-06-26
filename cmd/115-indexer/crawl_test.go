package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"five/internal/model"
	"five/internal/store"
)

type stubCrawlRunner struct{ err error }

func (s stubCrawlRunner) CrawlShare(_ context.Context, _ model.Share, _ int64) error {
	return s.err
}

func TestRunCrawlOnceMarksShareCompleted(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := runCrawlOnce(ctx, s, stubCrawlRunner{}, "sw1", "rc1", time.Now().Unix()); err != nil {
		t.Fatalf("run crawl once: %v", err)
	}

	got, ok, err := s.GetShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("get share: ok=%v err=%v", ok, err)
	}
	if got.Status != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED", got.Status)
	}
	if got.ReceiveCode != "rc1" {
		t.Fatalf("receive_code = %q, want rc1", got.ReceiveCode)
	}
}

func TestRunCrawlOncePropagatesCrawlError(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	if err := runCrawlOnce(ctx, s, stubCrawlRunner{err: errors.New("boom")}, "sw1", "rc1", 1); err == nil {
		t.Fatal("expected crawl error to propagate, got nil")
	}

	// Registered as ACTIVE before the crawl, but NOT marked COMPLETED.
	got, ok, err := s.GetShare(ctx, "sw1")
	if err != nil || !ok {
		t.Fatalf("get share: ok=%v err=%v", ok, err)
	}
	if got.Status != "ACTIVE" {
		t.Fatalf("status = %q, want ACTIVE (crawl failed before completion)", got.Status)
	}
}

func TestRunCrawlOnceReCrawlsCompletedShare(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// A share that already reached COMPLETED (e.g. forced re-crawl via the CLI).
	if err := runCrawlOnce(ctx, s, stubCrawlRunner{}, "sw1", "rc1", 100); err != nil {
		t.Fatalf("first crawl: %v", err)
	}
	if got, _, _ := s.GetShare(ctx, "sw1"); got.Status != "COMPLETED" {
		t.Fatalf("precondition: status = %q, want COMPLETED", got.Status)
	}

	// Re-running crawls it again and lands back on COMPLETED.
	if err := runCrawlOnce(ctx, s, stubCrawlRunner{}, "sw1", "rc1", 200); err != nil {
		t.Fatalf("second crawl: %v", err)
	}
	got, _, _ := s.GetShare(ctx, "sw1")
	if got.Status != "COMPLETED" {
		t.Fatalf("status = %q, want COMPLETED after re-crawl", got.Status)
	}
	if got.LastCrawledAt != 200 {
		t.Fatalf("last_crawled_at = %d, want 200", got.LastCrawledAt)
	}
}
