package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"five/internal/api115"
	"five/internal/model"
)

type registryStore struct {
	shares        []model.Share
	markedCrawled []string
	markedFailed  []string
	markedDead    []string
}

func (r *registryStore) ListSharesForCrawl(_ context.Context, _ int64) ([]model.Share, error) {
	return r.shares, nil
}

func (r *registryStore) MarkShareCrawled(_ context.Context, shareCode string, _ int64) error {
	r.markedCrawled = append(r.markedCrawled, shareCode)
	return nil
}

func (r *registryStore) RecordShareFailure(_ context.Context, shareCode, _ string) error {
	r.markedFailed = append(r.markedFailed, shareCode)
	return nil
}

func (r *registryStore) MarkShareDead(_ context.Context, shareCode, _ string) error {
	r.markedDead = append(r.markedDead, shareCode)
	return nil
}

type crawlRunner struct {
	err error
}

func (c crawlRunner) CrawlShare(context.Context, model.Share, int64) error {
	return c.err
}

func TestSchedulerRunOnceMarksSuccessFailureAndDead(t *testing.T) {
	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "ok", ReceiveCode: "a"},
		},
	}
	s := New(store, crawlRunner{})
	if err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once success: %v", err)
	}
	if len(store.markedCrawled) != 1 || store.markedCrawled[0] != "ok" {
		t.Fatalf("marked crawled = %#v", store.markedCrawled)
	}

	store = &registryStore{
		shares: []model.Share{
			{ShareCode: "stale", ReceiveCode: "a"},
		},
	}
	s = New(store, crawlRunner{err: errors.New("timeout")})
	if err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once stale: %v", err)
	}
	if len(store.markedFailed) != 1 || store.markedFailed[0] != "stale" {
		t.Fatalf("marked failed = %#v", store.markedFailed)
	}

	store = &registryStore{
		shares: []model.Share{
			{ShareCode: "dead", ReceiveCode: "a"},
		},
	}
	s = New(store, crawlRunner{err: api115.ErrDeadShare})
	if err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once dead: %v", err)
	}
	if len(store.markedDead) != 1 || store.markedDead[0] != "dead" {
		t.Fatalf("marked dead = %#v", store.markedDead)
	}
}

type emptyRegistry struct{}

func (emptyRegistry) ListSharesForCrawl(context.Context, int64) ([]model.Share, error) {
	return nil, nil
}
func (emptyRegistry) MarkShareCrawled(context.Context, string, int64) error    { return nil }
func (emptyRegistry) RecordShareFailure(context.Context, string, string) error { return nil }
func (emptyRegistry) MarkShareDead(context.Context, string, string) error      { return nil }

type countingRunner struct {
	calls *atomic.Int32
}

func (c countingRunner) CrawlShare(context.Context, model.Share, int64) error {
	c.calls.Add(1)
	return nil
}

func TestSchedulerLoopStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s := New(emptyRegistry{}, countingRunner{calls: &atomic.Int32{}})
	done := make(chan error, 1)
	go func() {
		done <- s.RunLoop(ctx, 10*time.Millisecond)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run loop returned err: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("scheduler loop did not stop on context cancel")
	}
}
