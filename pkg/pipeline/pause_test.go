package pipeline

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
				err := g.WaitIfPaused(t.Context())
				if diff := cmp.Diff(nil, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("WaitIfPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "WaitIfPaused blocks when paused and unblocks on Resume",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()
				if diff := cmp.Diff(true, g.IsPaused()); diff != "" {
					t.Fatalf("IsPaused() diff (-want +got):\n%s", diff)
				}

				startedWaiting := make(chan struct{})
				resumed := make(chan struct{})

				go func() {
					close(startedWaiting)
					err := g.WaitIfPaused(t.Context())
					if diff := cmp.Diff(nil, err, cmpopts.EquateErrors()); diff != "" {
						t.Errorf("WaitIfPaused() diff (-want +got):\n%s", diff)
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
				if diff := cmp.Diff(false, g.IsPaused()); diff != "" {
					t.Fatalf("IsPaused() diff (-want +got):\n%s", diff)
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
				if diff := cmp.Diff(context.Canceled, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("WaitIfPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "idempotent pause calls",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()
				g.Pause()
				if diff := cmp.Diff(true, g.IsPaused()); diff != "" {
					t.Errorf("IsPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "idempotent resume calls",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Resume()
				g.Resume()
				if diff := cmp.Diff(false, g.IsPaused()); diff != "" {
					t.Errorf("IsPaused() diff (-want +got):\n%s", diff)
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
				if diff := cmp.Diff(false, g.IsPaused()); diff != "" {
					t.Errorf("IsPaused() diff (-want +got):\n%s", diff)
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
							err := g.WaitIfPaused(ctx)
							if err != nil && cmp.Diff(context.Canceled, err, cmpopts.EquateErrors()) != "" {
								t.Errorf("WaitIfPaused() unexpected error = %v", err)
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

				if diff := cmp.Diff(int64(totalCycles), completedCycles.Load()); diff != "" {
					t.Errorf("completedCycles diff (-want +got):\n%s", diff)
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
