package pipeline

import (
	"context"
	"sync"
)

// PauseGate is a concurrency-safe pause/resume gate.
// When not paused, WaitIfPaused returns nil immediately.
// When paused, WaitIfPaused blocks until Resume is called or ctx is cancelled.
type PauseGate struct {
	mu       sync.Mutex
	paused   bool
	resumeCh chan struct{}
}

// NewPauseGate creates a new PauseGate in the unpaused state.
func NewPauseGate() *PauseGate {
	return &PauseGate{}
}

// Pause pauses the gate. If already paused, Pause is a no-op.
func (g *PauseGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		g.paused = true
		g.resumeCh = make(chan struct{})
	}
}

// Resume unpauses the gate and unblocks all waiting callers.
// If not paused, Resume is a no-op.
func (g *PauseGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		g.paused = false
		close(g.resumeCh)
		g.resumeCh = nil
	}
}

// IsPaused reports whether the gate is currently paused.
func (g *PauseGate) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// WaitIfPaused blocks if the gate is currently paused until Resume is called
// or ctx is cancelled. If not paused, it returns nil immediately.
func (g *PauseGate) WaitIfPaused(ctx context.Context) error {
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return nil
	}
	ch := g.resumeCh
	g.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
