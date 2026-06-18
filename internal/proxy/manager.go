package proxy

import (
	"context"
	"sync"
	"time"

	"five/internal/api115"
)

type State string

const (
	StateActive   State = "ACTIVE"
	StateBlocked  State = "BLOCKED"
	StateCooldown State = "COOLDOWN"
)

type Config struct {
	FailureThreshold int
	Now              func() time.Time
}

type Proxy struct {
	ID           string
	URL          string
	State        State
	FailureCount int
	Deadline     time.Time
}

type Manager struct {
	mu      sync.Mutex
	cfg     Config
	proxies []Proxy
	next    int
}

func New(cfg Config) *Manager {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 3
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg}
}

func (m *Manager) Add(id string) {
	m.AddWithURL(id, "")
}

func (m *Manager) AddWithURL(id, url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.proxies = append(m.proxies, Proxy{ID: id, URL: url, State: StateActive})
}

func (m *Manager) AddWithProxy(proxy Proxy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if proxy.State == "" {
		proxy.State = StateActive
	}
	m.proxies = append(m.proxies, proxy)
}

func (m *Manager) AcquireProxy() (Proxy, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.proxies) == 0 {
		return Proxy{}, false
	}
	m.pruneExpired()
	for range m.proxies {
		p := m.proxies[m.next%len(m.proxies)]
		m.next++
		if p.State != StateBlocked {
			return p, true
		}
	}
	return Proxy{}, false
}

func (m *Manager) Acquire() (api115.ProxyRef, bool) {
	p, ok := m.AcquireProxy()
	if !ok {
		return api115.ProxyRef{}, false
	}
	return api115.ProxyRef{
		ID:  p.ID,
		URL: p.URL,
	}, true
}

func (m *Manager) RecordFailure(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.proxies {
		if m.proxies[i].ID != id {
			continue
		}
		m.proxies[i].FailureCount++
		if m.proxies[i].FailureCount >= m.cfg.FailureThreshold {
			m.proxies[i].State = StateBlocked
		}
		return
	}
}

func (m *Manager) RecordSuccess(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.proxies {
		if m.proxies[i].ID != id {
			continue
		}
		m.proxies[i].FailureCount = 0
		m.proxies[i].State = StateActive
		return
	}
}

func (m *Manager) Recover(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.proxies {
		if m.proxies[i].ID != id {
			continue
		}
		m.proxies[i].State = StateCooldown
		return
	}
}

func (m *Manager) State(id string) State {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.proxies {
		if p.ID == id {
			return p.State
		}
	}
	return ""
}

type Fetcher interface {
	Fetch(ctx context.Context) (Proxy, error)
}

type Validator interface {
	Validate(ctx context.Context, proxy Proxy) bool
}

func (m *Manager) EnsureCapacity(ctx context.Context, provider Fetcher, validator Validator, minAvailable int) error {
	if minAvailable <= 0 || provider == nil {
		return nil
	}
	m.mu.Lock()
	m.pruneExpired()
	m.mu.Unlock()
	for {
		m.mu.Lock()
		count := m.availableCount()
		m.mu.Unlock()
		if count >= minAvailable {
			break
		}
		p, err := provider.Fetch(ctx)
		if err != nil {
			return err
		}
		if validator != nil && !validator.Validate(ctx, p) {
			continue
		}
		m.AddWithProxy(p)
	}
	return nil
}

func (m *Manager) pruneExpired() {
	now := m.cfg.Now()
	keep := m.proxies[:0]
	for _, p := range m.proxies {
		if !p.Deadline.IsZero() && !p.Deadline.After(now) {
			continue
		}
		keep = append(keep, p)
	}
	m.proxies = keep
	if m.next >= len(m.proxies) {
		m.next = 0
	}
}

func (m *Manager) availableCount() int {
	count := 0
	for _, p := range m.proxies {
		if p.State != StateBlocked {
			count++
		}
	}
	return count
}
