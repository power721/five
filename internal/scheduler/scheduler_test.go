package scheduler

import (
	"bytes"
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
	s := New(store, crawlRunner{}, nil)
	proxyFailureOnly, err := s.RunOnce(context.Background(), 1)
	if err != nil {
		t.Fatalf("run once success: %v", err)
	}
	if proxyFailureOnly {
		t.Fatal("expected success run not to report proxy failure only")
	}
	if len(store.markedCrawled) != 1 || store.markedCrawled[0] != "ok" {
		t.Fatalf("marked crawled = %#v", store.markedCrawled)
	}

	store = &registryStore{
		shares: []model.Share{
			{ShareCode: "stale", ReceiveCode: "a"},
		},
	}
	s = New(store, crawlRunner{err: errors.New("timeout")}, nil)
	proxyFailureOnly, err = s.RunOnce(context.Background(), 1)
	if err != nil {
		t.Fatalf("run once stale: %v", err)
	}
	if proxyFailureOnly {
		t.Fatal("expected non-proxy failure run not to report proxy failure only")
	}
	if len(store.markedFailed) != 1 || store.markedFailed[0] != "stale" {
		t.Fatalf("marked failed = %#v", store.markedFailed)
	}

	store = &registryStore{
		shares: []model.Share{
			{ShareCode: "dead", ReceiveCode: "a"},
		},
	}
	s = New(store, crawlRunner{err: api115.ErrDeadShare}, nil)
	proxyFailureOnly, err = s.RunOnce(context.Background(), 1)
	if err != nil {
		t.Fatalf("run once dead: %v", err)
	}
	if proxyFailureOnly {
		t.Fatal("expected dead share run not to report proxy failure only")
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

	s := New(emptyRegistry{}, countingRunner{calls: &atomic.Int32{}}, nil)
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

func TestSchedulerLogsShareOutcomes(t *testing.T) {
	var buf bytes.Buffer
	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "ok", ReceiveCode: "a"},
		},
	}
	s := New(store, crawlRunner{}, &buf)
	if _, err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once success: %v", err)
	}
	output := buf.String()
	if output == "" {
		t.Fatal("expected scheduler to write logs")
	}
	if !bytes.Contains([]byte(output), []byte("share=ok")) || !bytes.Contains([]byte(output), []byte("result=success")) {
		t.Fatalf("unexpected success log: %q", output)
	}

	buf.Reset()
	store = &registryStore{
		shares: []model.Share{
			{ShareCode: "dead", ReceiveCode: "a"},
		},
	}
	s = New(store, crawlRunner{err: api115.ErrDeadShare}, &buf)
	if _, err := s.RunOnce(context.Background(), 1); err != nil {
		t.Fatalf("run once dead: %v", err)
	}
	output = buf.String()
	if !bytes.Contains([]byte(output), []byte("share=dead")) || !bytes.Contains([]byte(output), []byte("result=dead")) {
		t.Fatalf("unexpected dead log: %q", output)
	}
}

func TestSchedulerContinuesRunOnceOnProxyFailure(t *testing.T) {
	var buf bytes.Buffer
	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "first", ReceiveCode: "a"},
			{ShareCode: "second", ReceiveCode: "b"},
		},
	}
	s := New(store, crawlRunner{err: api115.WrapError(api115.KindProxyFailure, "proxy pool exhausted", 0, nil)}, &buf)
	proxyFailureOnly, err := s.RunOnce(context.Background(), 1)
	if err != nil {
		t.Fatalf("run once err = %v, want nil", err)
	}
	if !proxyFailureOnly {
		t.Fatal("expected proxy failure only run to report proxy failure only")
	}
	if len(store.markedFailed) != 2 || store.markedFailed[0] != "first" || store.markedFailed[1] != "second" {
		t.Fatalf("marked failed = %#v", store.markedFailed)
	}
	if !bytes.Contains(buf.Bytes(), []byte("share=second")) {
		t.Fatalf("expected second share processing in logs: %q", buf.String())
	}
}

func TestSchedulerLoopBacksOffAfterProxyFailureOnlyRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "first", ReceiveCode: "a"},
		},
	}
	s := New(store, crawlRunner{err: api115.WrapError(api115.KindProxyFailure, "proxy pool exhausted", 0, nil)}, nil)
	var sleeps []time.Duration
	s.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		cancel()
		return nil
	}
	s.now = func() time.Time { return time.Unix(100, 0) }

	if err := s.RunLoop(ctx, time.Minute); err != nil {
		t.Fatalf("run loop err = %v, want nil", err)
	}
	if len(sleeps) != 1 || sleeps[0] != 5*time.Minute {
		t.Fatalf("sleep durations = %#v, want [5m]", sleeps)
	}
}

func TestSchedulerProxyBackoffResetsAfterSuccessfulRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &registryStore{
		shares: []model.Share{
			{ShareCode: "first", ReceiveCode: "a"},
		},
	}
	runner := &sequenceRunner{
		errs: []error{
			api115.WrapError(api115.KindProxyFailure, "proxy pool exhausted", 0, nil),
			nil,
		},
	}
	s := New(store, runner, nil)
	var sleeps []time.Duration
	s.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		if len(sleeps) == 2 {
			cancel()
		}
		return nil
	}
	var tick int64
	s.now = func() time.Time {
		tick++
		return time.Unix(100+tick, 0)
	}

	if err := s.RunLoop(ctx, time.Minute); err != nil {
		t.Fatalf("run loop err = %v, want nil", err)
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleep count = %d, want 2", len(sleeps))
	}
	if sleeps[0] != 5*time.Minute {
		t.Fatalf("first sleep = %s, want 5m", sleeps[0])
	}
	if sleeps[1] != time.Minute {
		t.Fatalf("second sleep = %s, want 1m", sleeps[1])
	}
}

type sequenceRunner struct {
	errs  []error
	calls int
}

func (r *sequenceRunner) CrawlShare(context.Context, model.Share, int64) error {
	if r.calls >= len(r.errs) {
		return nil
	}
	err := r.errs[r.calls]
	r.calls++
	return err
}
