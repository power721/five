package proxy

import (
	"bytes"
	"context"
	"errors"
	"log"
	"testing"
	"time"
)

type staticProvider struct {
	proxy Proxy
	calls int
	err   error
}

func (s *staticProvider) Fetch(context.Context) (Proxy, error) {
	s.calls++
	if s.err != nil {
		return Proxy{}, s.err
	}
	return s.proxy, nil
}

type sequenceProvider struct {
	proxies []Proxy
	calls   int
}

func (s *sequenceProvider) Fetch(context.Context) (Proxy, error) {
	p := s.proxies[s.calls]
	s.calls++
	return p, nil
}

type staticValidator struct {
	results map[string]bool
	calls   int
}

func (s *staticValidator) Validate(_ context.Context, proxy Proxy) bool {
	s.calls++
	return s.results[proxy.ID]
}

func TestManagerAcquireFetchesActiveProxyOnDemand(t *testing.T) {
	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 3,
		Now:              func() time.Time { return now },
	})
	provider := &staticProvider{
		proxy: Proxy{
			ID:       "p1",
			URL:      "http://p1",
			State:    StateActive,
			Deadline: now.Add(3 * time.Minute),
		},
	}

	ref, ok := mgr.Acquire(context.Background(), provider, nil)
	if !ok {
		t.Fatal("expected manager to fetch proxy on demand")
	}
	if ref.ID != "p1" {
		t.Fatalf("acquired proxy = %#v", ref)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
}

func TestManagerAcquireReplacesBlockedProxy(t *testing.T) {
	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 1,
		Now:              func() time.Time { return now },
	})
	mgr.current = &Proxy{
		ID:       "bad",
		URL:      "http://bad",
		State:    StateActive,
		Deadline: now.Add(3 * time.Minute),
	}
	mgr.RecordFailure("bad")

	provider := &staticProvider{
		proxy: Proxy{
			ID:       "good",
			URL:      "http://good",
			State:    StateActive,
			Deadline: now.Add(3 * time.Minute),
		},
	}

	ref, ok := mgr.Acquire(context.Background(), provider, nil)
	if !ok {
		t.Fatal("expected replacement proxy")
	}
	if ref.ID != "good" {
		t.Fatalf("acquired proxy = %#v, want good", ref)
	}
}

func TestManagerAcquireSkipsProxyThatFailsValidation(t *testing.T) {
	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 1,
		Now:              func() time.Time { return now },
	})
	provider := &sequenceProvider{
		proxies: []Proxy{
			{ID: "bad", URL: "http://bad", State: StateActive, Deadline: now.Add(3 * time.Minute)},
			{ID: "good", URL: "http://good", State: StateActive, Deadline: now.Add(3 * time.Minute)},
		},
	}
	validator := &staticValidator{
		results: map[string]bool{
			"bad":  false,
			"good": true,
		},
	}

	ref, ok := mgr.Acquire(context.Background(), provider, validator)
	if !ok {
		t.Fatal("expected validated proxy")
	}
	if ref.ID != "good" {
		t.Fatalf("acquired proxy = %#v, want good", ref)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if validator.calls != 2 {
		t.Fatalf("validator calls = %d, want 2", validator.calls)
	}
}

func TestManagerRecordSuccessResetsFailureCount(t *testing.T) {
	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 3,
		Now:              func() time.Time { return now },
	})
	mgr.current = &Proxy{
		ID:       "p1",
		URL:      "http://p1",
		State:    StateActive,
		Deadline: now.Add(3 * time.Minute),
	}

	mgr.RecordFailure("p1")
	mgr.RecordFailure("p1")
	mgr.RecordSuccess("p1")

	if mgr.current == nil {
		t.Fatal("expected current proxy")
	}
	if mgr.current.FailureCount != 0 {
		t.Fatalf("failure count = %d, want 0", mgr.current.FailureCount)
	}
	if mgr.consecutiveReplacements != 0 {
		t.Fatalf("replacement streak = %d, want 0", mgr.consecutiveReplacements)
	}
}

func TestManagerTripsFatalAfterFiveConsecutiveReplacements(t *testing.T) {
	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 1,
		FatalThreshold:   5,
		Now:              func() time.Time { return now },
	})

	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		mgr.current = &Proxy{
			ID:       id,
			URL:      "http://" + id,
			State:    StateActive,
			Deadline: now.Add(3 * time.Minute),
		}
		mgr.RecordFailure(id)
	}

	if _, ok := mgr.Acquire(context.Background(), &staticProvider{}, nil); ok {
		t.Fatal("expected manager to stop after fatal threshold")
	}
	if !errors.Is(mgr.FatalError(), ErrProxyFatal) {
		t.Fatalf("fatal err = %v", mgr.FatalError())
	}
}

func TestManagerLogsProxyFatalThreshold(t *testing.T) {
	var logs bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logs)
	log.SetFlags(0)
	defer log.SetOutput(prevWriter)
	defer log.SetFlags(prevFlags)

	now := time.Date(2026, 6, 18, 21, 40, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 1,
		FatalThreshold:   2,
		Now:              func() time.Time { return now },
	})
	for i := 0; i < 2; i++ {
		id := string(rune('a' + i))
		mgr.current = &Proxy{
			ID:       id,
			URL:      "http://" + id,
			State:    StateActive,
			Deadline: now.Add(3 * time.Minute),
		}
		mgr.RecordFailure(id)
	}

	output := logs.String()
	if !bytes.Contains([]byte(output), []byte("event=proxy_fatal")) || !bytes.Contains([]byte(output), []byte("consecutive_replacements=2")) {
		t.Fatalf("missing proxy fatal log: %q", output)
	}
}
