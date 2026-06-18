package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

type Registry struct {
	mu       sync.RWMutex
	gauges   map[string]float64
	counters map[string]float64
}

func New() *Registry {
	return &Registry{
		gauges:   map[string]float64{},
		counters: map[string]float64{},
	}
}

func (r *Registry) SetGauge(name string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = value
}

func (r *Registry) IncCounter(name string, delta float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters[name] += delta
}

func (r *Registry) Snapshot() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var lines []string
	for name, value := range r.gauges {
		lines = append(lines, fmt.Sprintf("%s %g", name, value))
	}
	for name, value := range r.counters {
		lines = append(lines, fmt.Sprintf("%s %g", name, value))
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(r.Snapshot()))
	})
}
