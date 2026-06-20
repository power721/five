package proxy

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"five/internal/api115"
)

type State string

const (
	StateActive  State = "ACTIVE"
	StateBlocked State = "BLOCKED"
)

var ErrProxyFatal = errors.New("proxy subsystem stopped after consecutive replacements")

type Config struct {
	FailureThreshold int
	FatalThreshold   int
	Now              func() time.Time
}

type Proxy struct {
	ID           string
	URL          string
	State        State
	FailureCount int
	Deadline     time.Time
}

type Fetcher interface {
	Fetch(ctx context.Context) (Proxy, error)
}

type Validator interface {
	Validate(ctx context.Context, proxy Proxy) bool
}

type Manager struct {
	mu                      sync.Mutex
	cfg                     Config
	current                 *Proxy
	consecutiveReplacements int
	fatalErr                error
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

func (m *Manager) Acquire(ctx context.Context, provider Fetcher, validator Validator) (api115.ProxyRef, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.fatalErr != nil {
		return api115.ProxyRef{}, false
	}
	if m.current != nil && m.current.State == StateActive && !m.isExpired(*m.current) {
		return api115.ProxyRef{ID: m.current.ID, URL: m.current.URL}, true
	}
	m.current = nil
	if provider == nil {
		return api115.ProxyRef{}, false
	}
	for {
		p, err := provider.Fetch(ctx)
		if err != nil {
			return api115.ProxyRef{}, false
		}
		if validator != nil && !validator.Validate(ctx, p) {
			continue
		}
		if p.State == "" {
			p.State = StateActive
		}
		m.current = &p
		return api115.ProxyRef{ID: p.ID, URL: p.URL}, true
	}
}

func (m *Manager) RecordFailure(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != id {
		return
	}
	m.current.FailureCount++
	if m.current.FailureCount < m.cfg.FailureThreshold {
		return
	}
	m.current.State = StateBlocked
	m.current = nil
	m.consecutiveReplacements++
	if m.cfg.FatalThreshold > 0 && m.consecutiveReplacements >= m.cfg.FatalThreshold {
		m.fatalErr = ErrProxyFatal
		log.Printf("event=proxy_fatal consecutive_replacements=%d threshold=%d", m.consecutiveReplacements, m.cfg.FatalThreshold)
	}
}

func (m *Manager) RecordSuccess(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != id {
		return
	}
	m.current.FailureCount = 0
	m.current.State = StateActive
	m.consecutiveReplacements = 0
}

func (m *Manager) FatalError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fatalErr
}

func (m *Manager) isExpired(proxy Proxy) bool {
	if proxy.Deadline.IsZero() {
		return false
	}
	return !proxy.Deadline.After(m.cfg.Now())
}
