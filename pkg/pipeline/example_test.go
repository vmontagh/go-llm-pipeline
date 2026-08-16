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

// ExamplePauseGate shows the pause/resume lifecycle of a PauseGate. A worker
// blocks in WaitIfPaused while the gate is paused and is released once Resume
// is called. Synchronization is done entirely through the public API and a
// channel so the output is deterministic.
func ExamplePauseGate() {
	gate := pipeline.NewPauseGate()

	gate.Pause()
	fmt.Println("paused:", gate.IsPaused())

	released := make(chan error, 1)
	go func() {
		released <- gate.WaitIfPaused(context.Background())
	}()

	gate.Resume()
	err := <-released

	fmt.Println("still paused:", gate.IsPaused())
	fmt.Println("worker released, err:", err)

	// Output:
	// paused: true
	// still paused: false
	// worker released, err: <nil>
}
