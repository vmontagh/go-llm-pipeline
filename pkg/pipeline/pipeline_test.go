package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConsume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "nil process is a no-op",
			run: func(t *testing.T) {
				input := make(chan int)
				Consume(t.Context(), input, 1, nil, nil)
			},
		},
		{
			name: "processes all items concurrently",
			run: func(t *testing.T) {
				input := make(chan int, 3)
				input <- 1
				input <- 2
				input <- 3
				close(input)

				var mu sync.Mutex
				var processed []int

				process := func(_ context.Context, item int) {
					mu.Lock()
					processed = append(processed, item)
					mu.Unlock()
				}

				var wg sync.WaitGroup
				Consume(t.Context(), input, 2, &wg, process)
				wg.Wait()

				if len(processed) != 3 {
					t.Errorf("len(processed) = %d, want 3", len(processed))
				}
			},
		},
		{
			name: "with nil pause gate option is a no-op",
			run: func(t *testing.T) {
				input := make(chan int, 2)
				input <- 10
				input <- 20
				close(input)

				var count atomic.Int64
				var wg sync.WaitGroup

				Consume(t.Context(), input, 2, &wg, func(_ context.Context, _ int) {
					count.Add(1)
				}, WithPauseGate(nil))

				wg.Wait()

				if got := count.Load(); got != 2 {
					t.Fatalf("count = %d, want 2", got)
				}
			},
		},
		{
			name: "with pause gate blocks new work until resumed",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Pause()

				input := make(chan int, 2)
				input <- 1
				input <- 2
				close(input)

				var count atomic.Int64
				var wg sync.WaitGroup
				started := make(chan struct{})

				process := func(_ context.Context, _ int) {
					count.Add(1)
					select {
					case started <- struct{}{}:
					default:
					}
				}

				Consume(t.Context(), input, 2, &wg, process, WithPauseGate(gate))

				select {
				case <-started:
					t.Fatal("worker processed item before Resume()")
				default:
				}

				if got := count.Load(); got != 0 {
					t.Fatalf("count before resume = %d, want 0", got)
				}

				gate.Resume()
				wg.Wait()

				if got := count.Load(); got != 2 {
					t.Fatalf("count after resume = %d, want 2", got)
				}
			},
		},
		{
			name: "pausing mid-stream lets in-flight finish and halts next item",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				input := make(chan int, 5)

				var count atomic.Int64
				var wg sync.WaitGroup
				item1Started := make(chan struct{})
				item1Continue := make(chan struct{})
				item1Finished := make(chan struct{})
				item2Started := make(chan struct{})

				process := func(_ context.Context, item int) {
					if item == 1 {
						close(item1Started)
						<-item1Continue
					}
					if item == 2 {
						close(item2Started)
					}
					count.Add(1)
					if item == 1 {
						close(item1Finished)
					}
				}

				Consume(t.Context(), input, 1, &wg, process, WithPauseGate(gate))

				input <- 1
				<-item1Started // Item 1 is actively running

				// Pause gate while item 1 is active
				gate.Pause()
				input <- 2
				close(input)

				// Finish item 1
				close(item1Continue)
				<-item1Finished

				// Item 2 must not start while gate is paused
				select {
				case <-item2Started:
					t.Fatal("item 2 started processing while gate was paused")
				default:
				}

				if got := count.Load(); got != 1 {
					t.Fatalf("count while paused = %d, want 1", got)
				}

				// Resume gate -> item 2 starts and completes
				gate.Resume()
				<-item2Started
				wg.Wait()

				if got := count.Load(); got != 2 {
					t.Fatalf("count after resume = %d, want 2", got)
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
