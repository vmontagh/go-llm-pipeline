package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPauseGate_WaitIfPaused_NotPaused(t *testing.T) {
	t.Parallel()
	g := NewPauseGate()
	if err := g.WaitIfPaused(t.Context()); err != nil {
		t.Fatalf("WaitIfPaused() error = %v, want nil", err)
	}
}

func TestPauseGate_PauseAndResume(t *testing.T) {
	t.Parallel()
	g := NewPauseGate()
	g.Pause()
	if !g.IsPaused() {
		t.Fatalf("IsPaused() = false, want true")
	}

	resumed := make(chan struct{})
	go func() {
		if err := g.WaitIfPaused(t.Context()); err != nil {
			t.Errorf("WaitIfPaused() error = %v, want nil", err)
		}
		close(resumed)
	}()

	select {
	case <-resumed:
		t.Fatal("WaitIfPaused() unblocked before Resume()")
	case <-time.After(50 * time.Millisecond):
	}

	g.Resume()
	if g.IsPaused() {
		t.Fatalf("IsPaused() = true, want false")
	}

	select {
	case <-resumed:
	case <-time.After(1 * time.Second):
		t.Fatal("WaitIfPaused() timed out waiting for Resume()")
	}
}

func TestPauseGate_ContextCancellation(t *testing.T) {
	t.Parallel()
	g := NewPauseGate()
	g.Pause()

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)

	go func() {
		errCh <- g.WaitIfPaused(ctx)
	}()

	select {
	case <-errCh:
		t.Fatal("WaitIfPaused() unblocked before cancel")
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitIfPaused() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("WaitIfPaused() timed out waiting for context cancel")
	}
}

func TestPauseGate_Idempotency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		actions    func(g *PauseGate)
		wantPaused bool
	}{
		{
			name: "double pause",
			actions: func(g *PauseGate) {
				g.Pause()
				g.Pause()
			},
			wantPaused: true,
		},
		{
			name: "double resume",
			actions: func(g *PauseGate) {
				g.Resume()
				g.Resume()
			},
			wantPaused: false,
		},
		{
			name: "pause then double resume",
			actions: func(g *PauseGate) {
				g.Pause()
				g.Resume()
				g.Resume()
			},
			wantPaused: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := NewPauseGate()
			tc.actions(g)
			if got := g.IsPaused(); got != tc.wantPaused {
				t.Errorf("IsPaused() = %v, want %v", got, tc.wantPaused)
			}
		})
	}
}

func TestPauseGate_Concurrent(t *testing.T) {
	t.Parallel()
	g := NewPauseGate()
	var wg sync.WaitGroup

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := g.WaitIfPaused(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("WaitIfPaused() error = %v", err)
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			g.Pause()
			time.Sleep(10 * time.Millisecond)
			g.Resume()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	wg.Wait()
}
