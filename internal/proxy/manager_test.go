package proxy

import (
	"context"
	"testing"
	"time"
)

func TestManagerBlockCooldownAndRecover(t *testing.T) {
	mgr := New(Config{
		FailureThreshold: 3,
	})
	mgr.Add("p1")

	if _, ok := mgr.Acquire(); !ok {
		t.Fatal("expected proxy to be available initially")
	}

	mgr.RecordFailure("p1")
	mgr.RecordFailure("p1")
	if state := mgr.State("p1"); state != StateActive {
		t.Fatalf("state after 2 failures = %q, want ACTIVE", state)
	}

	mgr.RecordFailure("p1")
	if state := mgr.State("p1"); state != StateBlocked {
		t.Fatalf("state after threshold failures = %q, want BLOCKED", state)
	}

	mgr.Recover("p1")
	if state := mgr.State("p1"); state != StateCooldown {
		t.Fatalf("state after recover = %q, want COOLDOWN", state)
	}

	mgr.RecordSuccess("p1")
	if state := mgr.State("p1"); state != StateActive {
		t.Fatalf("state after success = %q, want ACTIVE", state)
	}
}

func TestManagerAcquireSkipsBlocked(t *testing.T) {
	mgr := New(Config{
		FailureThreshold: 1,
	})
	mgr.Add("blocked")
	mgr.Add("active")
	mgr.RecordFailure("blocked")

	p, ok := mgr.Acquire()
	if !ok {
		t.Fatal("expected an active proxy")
	}
	if p.ID != "active" {
		t.Fatalf("acquired proxy = %q, want active", p.ID)
	}
}

func TestManagerAcquireSkipsExpiredProxy(t *testing.T) {
	now := time.Date(2026, 6, 18, 19, 10, 0, 0, time.UTC)
	mgr := New(Config{
		FailureThreshold: 1,
		Now: func() time.Time {
			return now
		},
	})
	mgr.AddWithProxy(Proxy{
		ID:       "expired",
		URL:      "http://expired",
		State:    StateActive,
		Deadline: now.Add(-time.Minute),
	})
	mgr.AddWithProxy(Proxy{
		ID:       "fresh",
		URL:      "http://fresh",
		State:    StateActive,
		Deadline: now.Add(2 * time.Minute),
	})

	p, ok := mgr.Acquire()
	if !ok {
		t.Fatal("expected fresh proxy")
	}
	if p.ID != "fresh" {
		t.Fatalf("acquired proxy = %q, want fresh", p.ID)
	}
}

type staticProvider struct {
	proxy Proxy
	calls int
}

func (s *staticProvider) Fetch(context.Context) (Proxy, error) {
	s.calls++
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

func TestManagerEnsureCapacityFetchesFromProvider(t *testing.T) {
	now := time.Date(2026, 6, 18, 19, 10, 0, 0, time.UTC)
	provider := &staticProvider{
		proxy: Proxy{
			ID:       "fetched",
			URL:      "http://fetched",
			State:    StateActive,
			Deadline: now.Add(3 * time.Minute),
		},
	}
	mgr := New(Config{
		FailureThreshold: 1,
		Now: func() time.Time {
			return now
		},
	})
	if err := mgr.EnsureCapacity(context.Background(), provider, nil, 1); err != nil {
		t.Fatalf("ensure capacity: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	p, ok := mgr.Acquire()
	if !ok || p.ID != "fetched" {
		t.Fatalf("acquired proxy = %#v ok=%v, want fetched", p, ok)
	}
}

func TestManagerEnsureCapacitySkipsProxyThatFailsValidation(t *testing.T) {
	now := time.Date(2026, 6, 18, 19, 10, 0, 0, time.UTC)
	provider := &sequenceProvider{
		proxies: []Proxy{
			{
				ID:       "bad",
				URL:      "http://bad",
				State:    StateActive,
				Deadline: now.Add(3 * time.Minute),
			},
			{
				ID:       "good",
				URL:      "http://good",
				State:    StateActive,
				Deadline: now.Add(3 * time.Minute),
			},
		},
	}
	validator := &staticValidator{
		results: map[string]bool{
			"bad":  false,
			"good": true,
		},
	}
	mgr := New(Config{
		FailureThreshold: 1,
		Now: func() time.Time {
			return now
		},
	})
	if err := mgr.EnsureCapacity(context.Background(), provider, validator, 1); err != nil {
		t.Fatalf("ensure capacity with validation: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if validator.calls != 2 {
		t.Fatalf("validator calls = %d, want 2", validator.calls)
	}
	p, ok := mgr.Acquire()
	if !ok || p.ID != "good" {
		t.Fatalf("acquired proxy = %#v ok=%v, want good", p, ok)
	}
}
