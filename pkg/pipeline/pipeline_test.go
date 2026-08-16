package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
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

				if diff := cmp.Diff(3, len(processed)); diff != "" {
					t.Errorf("len(processed) diff (-want +got):\n%s", diff)
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

				if diff := cmp.Diff(int64(2), count.Load()); diff != "" {
					t.Fatalf("count diff (-want +got):\n%s", diff)
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
				gateReached := make(chan struct{}, 2)
				gate.waitHook = func() { gateReached <- struct{}{} }

				process := func(_ context.Context, _ int) {
					count.Add(1)
					select {
					case started <- struct{}{}:
					default:
					}
				}

				Consume(t.Context(), input, 2, &wg, process, WithPauseGate(gate))

				<-gateReached // a worker reached the paused gate before any processing

				select {
				case <-started:
					t.Fatal("worker processed item before Resume()")
				default:
				}

				if diff := cmp.Diff(int64(0), count.Load()); diff != "" {
					t.Fatalf("count before resume diff (-want +got):\n%s", diff)
				}

				gate.Resume()
				wg.Wait()

				if diff := cmp.Diff(int64(2), count.Load()); diff != "" {
					t.Fatalf("count after resume diff (-want +got):\n%s", diff)
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
				gateReached := make(chan struct{}, 1)
				gate.waitHook = func() { gateReached <- struct{}{} }

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

				<-gateReached // worker pulled item 2 and blocked on the paused gate

				// Item 2 must not start while gate is paused
				select {
				case <-item2Started:
					t.Fatal("item 2 started processing while gate was paused")
				default:
				}

				if diff := cmp.Diff(int64(1), count.Load()); diff != "" {
					t.Fatalf("count while paused diff (-want +got):\n%s", diff)
				}

				// Resume gate -> item 2 starts and completes
				gate.Resume()
				<-item2Started
				wg.Wait()

				if diff := cmp.Diff(int64(2), count.Load()); diff != "" {
					t.Fatalf("count after resume diff (-want +got):\n%s", diff)
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
