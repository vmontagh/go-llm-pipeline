package pipeline_test

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

// ExampleConsume shows how to fan a channel of work items out across a pool of
// workers. Because workers run concurrently, the results are collected under a
// mutex and sorted before printing so the output is deterministic.
func ExampleConsume() {
	input := make(chan int)
	go func() {
		defer close(input)
		for i := 1; i <= 5; i++ {
			input <- i
		}
	}()

	var (
		mu      sync.Mutex
		results []int
		wg      sync.WaitGroup
	)

	pipeline.Consume(context.Background(), input, 3, &wg, func(_ context.Context, n int) {
		mu.Lock()
		results = append(results, n*n)
		mu.Unlock()
	})
	wg.Wait()

	sort.Ints(results)
	fmt.Println(results)

	// Output:
	// [1 4 9 16 25]
}

// ExamplePauseGate shows the pause/resume state transitions of a PauseGate.
// When the gate is not paused, WaitIfPaused returns immediately.
func ExamplePauseGate() {
	gate := pipeline.NewPauseGate()
	fmt.Println("initial paused:", gate.IsPaused())

	// Not paused: a worker proceeds without blocking.
	fmt.Println("wait while running:", gate.WaitIfPaused(context.Background()))

	gate.Pause()
	fmt.Println("after Pause:", gate.IsPaused())

	gate.Resume()
	fmt.Println("after Resume:", gate.IsPaused())

	// Output:
	// initial paused: false
	// wait while running: <nil>
	// after Pause: true
	// after Resume: false
}

// ExamplePauseGate_blocking shows that a paused gate blocks a worker in
// WaitIfPaused. Because the gate is never resumed, the only way the worker
// returns is when its context is cancelled, which makes the outcome
// deterministic regardless of goroutine scheduling.
func ExamplePauseGate_blocking() {
	gate := pipeline.NewPauseGate()
	gate.Pause()

	ctx, cancel := context.WithCancel(context.Background())
	released := make(chan error, 1)
	go func() {
		released <- gate.WaitIfPaused(ctx)
	}()

	// The worker cannot proceed while the gate is paused; cancelling the
	// context is the only thing that releases it here.
	cancel()
	fmt.Println("blocked worker returned:", <-released)

	// Output:
	// blocked worker returned: context canceled
}
