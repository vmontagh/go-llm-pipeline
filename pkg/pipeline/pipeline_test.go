package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
		{
			name: "stopped gate drains queued items and returns after channel close",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Stop()

				input := make(chan int, 3)
				input <- 1
				input <- 2
				input <- 3
				close(input)

				var count atomic.Int64
				var wg sync.WaitGroup

				process := func(ctx context.Context, _ int) {
					if err := ctx.Err(); err != nil {
						t.Errorf("process ctx.Err() = %v, want nil (ctx must stay live while draining)", err)
					}
					count.Add(1)
				}

				Consume(t.Context(), input, 2, &wg, process, WithPauseGate(gate))
				wg.Wait() // returns only after the input channel is drained and closed

				if diff := cmp.Diff(int64(3), count.Load()); diff != "" {
					t.Fatalf("drained count diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "stopping a paused gate drains in-flight and remaining items",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				input := make(chan int, 5)

				var count atomic.Int64
				var wg sync.WaitGroup
				item1Started := make(chan struct{})
				item1Continue := make(chan struct{})
				gateReached := make(chan struct{}, 1)
				gate.waitHook = func() { gateReached <- struct{}{} }

				process := func(ctx context.Context, item int) {
					if err := ctx.Err(); err != nil {
						t.Errorf("process ctx.Err() = %v, want nil (stop must not cancel ctx)", err)
					}
					if item == 1 {
						close(item1Started)
						<-item1Continue
					}
					count.Add(1)
				}

				Consume(t.Context(), input, 1, &wg, process, WithPauseGate(gate))

				input <- 1
				<-item1Started // item 1 is actively running

				// Pause while item 1 is in flight, then queue more work.
				gate.Pause()
				input <- 2
				input <- 3
				close(item1Continue) // let item 1 finish
				<-gateReached        // worker parked on the paused gate

				// Stop: unblock the worker, drain remaining items, no cancellation.
				gate.Stop()
				close(input)
				wg.Wait()

				if diff := cmp.Diff(int64(3), count.Load()); diff != "" {
					t.Fatalf("drained count diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "DrainComplete default processes every queued item after draining",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Stop()

				input := make(chan int, 3)
				input <- 1
				input <- 2
				input <- 3
				close(input)

				var count atomic.Int64
				var wg sync.WaitGroup

				process := func(_ context.Context, _ int) {
					count.Add(1)
				}

				// No WithDrainMode: DrainComplete is the zero-value default.
				Consume(t.Context(), input, 2, &wg, process, WithPauseGate(gate))
				wg.Wait()

				if diff := cmp.Diff(int64(3), count.Load()); diff != "" {
					t.Fatalf("DrainComplete count diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "DrainDiscard routes queued items to handler and never processes",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Stop()

				const items = 4
				input := make(chan int, items)
				for i := 0; i < items; i++ {
					input <- i
				}
				close(input)

				var processed atomic.Int64
				var mu sync.Mutex
				var discarded []int
				var wg sync.WaitGroup

				process := func(_ context.Context, _ int) {
					processed.Add(1)
				}
				drainSkip := func(v int) {
					mu.Lock()
					discarded = append(discarded, v)
					mu.Unlock()
				}

				Consume(t.Context(), input, 2, &wg, process,
					WithPauseGate(gate), WithDrainMode(DrainDiscard), WithDrainHandler(drainSkip))
				wg.Wait()

				if diff := cmp.Diff(int64(0), processed.Load()); diff != "" {
					t.Fatalf("processed count diff (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(items, len(discarded)); diff != "" {
					t.Fatalf("discarded count diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "DrainDiscard with no handler drops items and returns cleanly",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Stop()

				input := make(chan int, 3)
				input <- 1
				input <- 2
				input <- 3
				close(input)

				var processed atomic.Int64
				var wg sync.WaitGroup

				Consume(t.Context(), input, 2, &wg, func(_ context.Context, _ int) {
					processed.Add(1)
				}, WithPauseGate(gate), WithDrainMode(DrainDiscard))
				wg.Wait()

				if diff := cmp.Diff(int64(0), processed.Load()); diff != "" {
					t.Fatalf("processed count diff (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "WithDrainHandler nil fn is a no-op",
			run: func(t *testing.T) {
				gate := NewPauseGate()
				gate.Stop()

				input := make(chan int, 2)
				input <- 1
				input <- 2
				close(input)

				var processed atomic.Int64
				var wg sync.WaitGroup

				var nilFn func(int)
				Consume(t.Context(), input, 1, &wg, func(_ context.Context, _ int) {
					processed.Add(1)
				}, WithPauseGate(gate), WithDrainMode(DrainDiscard), WithDrainHandler(nilFn))
				wg.Wait()

				if diff := cmp.Diff(int64(0), processed.Load()); diff != "" {
					t.Fatalf("processed count diff (-want +got):\n%s", diff)
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

func TestConsumeAll(t *testing.T) {
	t.Parallel()
	var sum atomic.Int64

	err := ConsumeAll(t.Context(), []int64{1, 2, 3}, 2, func(_ context.Context, n int64) { sum.Add(n) })

	if err != nil {
		t.Fatalf("ConsumeAll() = %v", err)
	}
	if got := sum.Load(); got != 6 {
		t.Errorf("sum of processed items = %d, want 6", got)
	}
}

func TestConsumeAllCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := ConsumeAll(ctx, []int{1, 2, 3}, 2, func(context.Context, int) {})

	if diff := cmp.Diff(context.Canceled, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("ConsumeAll(cancelled) error (-want +got):\n%s", diff)
	}
}
