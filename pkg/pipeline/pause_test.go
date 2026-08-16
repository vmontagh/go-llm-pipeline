package pipeline

import (
	"context"
	"errors"
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
				g.waitHook = func() { close(startedWaiting) }

				go func() {
					err := g.WaitIfPaused(t.Context())
					if diff := cmp.Diff(nil, err, cmpopts.EquateErrors()); diff != "" {
						t.Errorf("WaitIfPaused() diff (-want +got):\n%s", diff)
					}
					close(resumed)
				}()

				<-startedWaiting // waiter has reached the blocked path

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
				g.waitHook = func() { close(startedWaiting) }

				go func() {
					errCh <- g.WaitIfPaused(ctx)
				}()

				<-startedWaiting // waiter has reached the blocked path

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
		{
			name: "WaitIfPaused returns ErrDraining once stopped",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Stop()

				if diff := cmp.Diff(true, g.IsStopping()); diff != "" {
					t.Fatalf("IsStopping() diff (-want +got):\n%s", diff)
				}

				err := g.WaitIfPaused(t.Context())
				if diff := cmp.Diff(ErrDraining, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("WaitIfPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "stop is not context cancellation",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Stop()

				err := g.WaitIfPaused(t.Context())
				if errors.Is(err, context.Canceled) {
					t.Errorf("WaitIfPaused() = %v, want not errors.Is(context.Canceled)", err)
				}
				if !errors.Is(err, ErrDraining) {
					t.Errorf("WaitIfPaused() = %v, want errors.Is(ErrDraining)", err)
				}
			},
		},
		{
			name: "Stop is idempotent and takes precedence over pause and resume",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Stop()
				g.Stop() // idempotent

				g.Pause()  // no-op once stopping
				g.Resume() // no-op once stopping

				if diff := cmp.Diff(true, g.IsStopping()); diff != "" {
					t.Errorf("IsStopping() diff (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(false, g.IsPaused()); diff != "" {
					t.Errorf("IsPaused() diff (-want +got):\n%s", diff)
				}

				err := g.WaitIfPaused(t.Context())
				if diff := cmp.Diff(ErrDraining, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("WaitIfPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "Stop unblocks paused waiters with ErrDraining",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()

				startedWaiting := make(chan struct{})
				g.waitHook = func() { close(startedWaiting) }

				errCh := make(chan error, 1)
				go func() {
					errCh <- g.WaitIfPaused(t.Context())
				}()

				<-startedWaiting // waiter is parked on the paused gate

				select {
				case <-errCh:
					t.Fatal("WaitIfPaused() unblocked before Stop()")
				default:
				}

				g.Stop()

				err := <-errCh
				if diff := cmp.Diff(ErrDraining, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("WaitIfPaused() diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "Waiters reflects parked callers",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Pause()

				const waiters = 3
				parked := make(chan struct{}, waiters)
				g.waitHook = func() { parked <- struct{}{} }

				var wg sync.WaitGroup
				for i := 0; i < waiters; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						err := g.WaitIfPaused(t.Context())
						if diff := cmp.Diff(ErrDraining, err, cmpopts.EquateErrors()); diff != "" {
							t.Errorf("WaitIfPaused() diff (-want +got):\n%s", diff)
						}
					}()
				}

				for i := 0; i < waiters; i++ {
					<-parked // all callers have parked and incremented the waiter count
				}

				if diff := cmp.Diff(waiters, g.Waiters()); diff != "" {
					t.Fatalf("Waiters() while parked diff (-want +got):\n%s", diff)
				}

				g.Stop()
				wg.Wait()

				if diff := cmp.Diff(0, g.Waiters()); diff != "" {
					t.Fatalf("Waiters() after unblock diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "Draining channel is closed after Stop",
			run: func(t *testing.T) {
				g := NewPauseGate()
				drain := g.Draining()

				select {
				case <-drain:
					t.Fatal("Draining() closed before Stop()")
				default:
				}

				g.Stop()

				select {
				case <-drain:
				default:
					t.Fatal("Draining() not closed after Stop()")
				}
			},
		},
		{
			name: "Draining called after Stop is already closed",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Stop()

				select {
				case <-g.Draining():
				default:
					t.Fatal("Draining() after Stop() should be already closed")
				}
			},
		},
		{
			name: "zero-value gate Draining works and closes on Stop",
			run: func(t *testing.T) {
				var g PauseGate
				drain := g.Draining()

				select {
				case <-drain:
					t.Fatal("Draining() closed before Stop() on zero-value gate")
				default:
				}

				g.Stop()

				select {
				case <-drain:
				default:
					t.Fatal("Draining() not closed after Stop() on zero-value gate")
				}
			},
		},
		{
			name: "repeated Stop does not double close Draining",
			run: func(t *testing.T) {
				g := NewPauseGate()
				g.Stop()
				g.Stop() // must not panic on a double close

				select {
				case <-g.Draining():
				default:
					t.Fatal("Draining() not closed after Stop()")
				}
			},
		},
		{
			name: "caller selecting on Draining unblocks on Stop",
			run: func(t *testing.T) {
				g := NewPauseGate()
				unblocked := make(chan struct{})

				go func() {
					<-g.Draining()
					close(unblocked)
				}()

				select {
				case <-unblocked:
					t.Fatal("Draining() waiter unblocked before Stop()")
				default:
				}

				g.Stop()
				<-unblocked
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