package pipeline

import (
	"context"
	"errors"
	"sync"
)

// ErrDraining is returned by WaitIfPaused once Stop has been called on the gate.
// It is distinct from context.Canceled so callers can stop enqueuing new work
// and drain in-flight items while leaving the request context alive.
var ErrDraining = errors.New("pipeline draining")

// PauseGate is a concurrency-safe pause/resume gate with a terminal drain state.
// When not paused, WaitIfPaused returns nil immediately. When paused,
// WaitIfPaused blocks until Resume is called or ctx is cancelled. Once Stop has
// been called the gate is draining: WaitIfPaused returns ErrDraining for all
// current and future waiters, and Pause/Resume become no-ops.
type PauseGate struct {
	mu       sync.Mutex
	paused   bool
	stopping bool
	resumeCh chan struct{}
	waiters  int

	// waitHook, if non-nil, is invoked just before WaitIfPaused blocks on a
	// paused gate. It lets tests synchronize on a caller actually reaching the
	// blocked path; it is never set in production code.
	waitHook func()
}

// NewPauseGate creates a new PauseGate in the unpaused state.
func NewPauseGate() *PauseGate {
	return &PauseGate{}
}

// Pause pauses the gate. If already paused or stopping, Pause is a no-op.
func (g *PauseGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return
	}
	if !g.paused {
		g.paused = true
		g.resumeCh = make(chan struct{})
	}
}

// Resume unpauses the gate and unblocks all waiting callers.
// If not paused or stopping, Resume is a no-op.
func (g *PauseGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return
	}
	if g.paused {
		g.paused = false
		close(g.resumeCh)
		g.resumeCh = nil
	}
}

// Stop transitions the gate into a terminal draining state and unblocks all
// current and future waiters with ErrDraining. Stop is idempotent and takes
// precedence over pause/resume: once stopped, Pause and Resume are no-ops and
// WaitIfPaused returns ErrDraining immediately.
func (g *PauseGate) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopping {
		return
	}
	g.stopping = true
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

// IsStopping reports whether the gate has been asked to stop (drain).
func (g *PauseGate) IsStopping() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopping
}

// Waiters reports the number of callers parked in WaitIfPaused. A caller is
// counted from just before it blocks until it returns, so a caller that has
// just been released may remain counted briefly until it unwinds. This lets
// tests wait until N callers are parked on the gate before calling Resume or
// Stop, with no sleeps.
func (g *PauseGate) Waiters() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiters
}

// WaitIfPaused returns ErrDraining if the gate is stopping. Otherwise, if the
// gate is paused, it blocks until Resume is called, Stop is called (returning
// ErrDraining), or ctx is cancelled (returning ctx.Err()). If neither paused
// nor stopping, it returns nil immediately.
func (g *PauseGate) WaitIfPaused(ctx context.Context) error {
	g.mu.Lock()
	if g.stopping {
		g.mu.Unlock()
		return ErrDraining
	}
	if !g.paused {
		g.mu.Unlock()
		return nil
	}
	ch := g.resumeCh
	hook := g.waitHook
	g.waiters++
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.waiters--
		g.mu.Unlock()
	}()

	if hook != nil {
		hook()
	}

	select {
	case <-ch:
		g.mu.Lock()
		stopping := g.stopping
		g.mu.Unlock()
		if stopping {
			return ErrDraining
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
