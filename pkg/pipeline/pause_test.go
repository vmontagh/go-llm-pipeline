package pipeline

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPauseGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "WaitIfPaused on unpaused gate returns nil immediately",
			run: func(t *testing.T) {
				g := NewPauseGate()
				if err := g.WaitIfPaused(t.Context()); err != nil {
					t.Fatalf("WaitIfPaused() error = %v, want nil", err)
				}
			},
		},
		{
			name: "WaitIfPaused blocks when paused and unblocks on Resume",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()
				if !g.IsPaused() {
					t.Fatalf("IsPaused() = false, want true")
				}

				startedWaiting := make(chan struct{})
				resumed := make(chan struct{})

				go func() {
					close(startedWaiting)
					if err := g.WaitIfPaused(t.Context()); err != nil {
						t.Errorf("WaitIfPaused() error = %v, want nil", err)
					}
					close(resumed)
				}()

				<-startedWaiting

				select {
				case <-resumed:
					t.Fatal("WaitIfPaused() unblocked before Resume()")
				default:
				}

				g.Resume()
				if g.IsPaused() {
					t.Fatalf("IsPaused() = true, want false")
				}

				<-resumed
			},
		},
		{
			name: "WaitIfPaused unblocks with context cancellation",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()

				ctx, cancel := context.WithCancel(t.Context())
				startedWaiting := make(chan struct{})
				errCh := make(chan error, 1)

				go func() {
					close(startedWaiting)
					errCh <- g.WaitIfPaused(ctx)
				}()

				<-startedWaiting

				select {
				case <-errCh:
					t.Fatal("WaitIfPaused() unblocked before cancel")
				default:
				}

				cancel()

				err := <-errCh
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("WaitIfPaused() error = %v, want %v", err, context.Canceled)
				}
			},
		},
		{
			name: "idempotent pause calls",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()
				g.Pause()
				if !g.IsPaused() {
					t.Errorf("IsPaused() = false, want true")
				}
			},
		},
		{
			name: "idempotent resume calls",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Resume()
				g.Resume()
				if g.IsPaused() {
					t.Errorf("IsPaused() = true, want false")
				}
			},
		},
		{
			name: "pause then double resume",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()
				g.Resume()
				g.Resume()
				if g.IsPaused() {
					t.Errorf("IsPaused() = true, want false")
				}
			},
		},
		{
			name: "concurrent pause resume and wait iterations",
			run: func(t *testing.T) {
				g := NewPauseGate()
				var wg sync.WaitGroup

				ctx, cancel := context.WithCancel(t.Context())
				const totalCycles = 100
				var completedCycles atomic.Int64

				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for ctx.Err() == nil {
							if err := g.WaitIfPaused(ctx); err != nil && !errors.Is(err, context.Canceled) {
								t.Errorf("WaitIfPaused() error = %v", err)
							}
							runtime.Gosched()
						}
					}()
				}

				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < totalCycles; i++ {
						g.Pause()
						runtime.Gosched()
						g.Resume()
						runtime.Gosched()
						completedCycles.Add(1)
					}
					cancel()
				}()

				wg.Wait()

				if got := completedCycles.Load(); got != totalCycles {
					t.Errorf("completedCycles = %d, want %d", got, totalCycles)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}
