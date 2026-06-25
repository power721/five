package scheduler

import "sync/atomic"

// PauseGate is a runtime (non-durable) on/off switch consulted by the scheduler
// loop and the crawler to pause and resume crawling without restarting the
// daemon. Operators flip it through the admin HTTP API; the state is lost on
// restart by design.
type PauseGate struct {
	paused atomic.Bool
}

func NewPauseGate() *PauseGate { return &PauseGate{} }

func (g *PauseGate) Pause()  { g.paused.Store(true) }
func (g *PauseGate) Resume() { g.paused.Store(false) }
func (g *PauseGate) Paused() bool { return g.paused.Load() }
