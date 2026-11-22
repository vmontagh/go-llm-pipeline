package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// ExamplePauseGate shows the three observable states of a PauseGate: running,
// paused, and draining. Once Stop is called the gate is terminally draining and
// Pause/Resume become no-ops.
func ExamplePauseGate() {
	gate := pipeline.NewPauseGate()
	fmt.Println("initial paused:", gate.IsPaused())

	// Not paused: a worker proceeds without blocking.
	fmt.Println("wait while running:", gate.WaitIfPaused(context.Background()))

	gate.Pause()
	fmt.Println("after Pause:", gate.IsPaused())

	gate.Resume()
	fmt.Println("after Resume:", gate.IsPaused())

	// Stop requests a graceful drain. WaitIfPaused now returns ErrDraining,
	// which is deliberately distinct from context.Canceled so in-flight work
	// finishes normally instead of being aborted.
	gate.Stop()
	err := gate.WaitIfPaused(context.Background())
	fmt.Println("stopping:", gate.IsStopping())
	fmt.Println("drain err is ErrDraining:", errors.Is(err, pipeline.ErrDraining))
	fmt.Println("drain err is context.Canceled:", errors.Is(err, context.Canceled))

	// Output:
	// initial paused: false
	// wait while running: <nil>
	// after Pause: true
	// after Resume: false
	// stopping: true
	// drain err is ErrDraining: true
	// drain err is context.Canceled: false
}

// ExamplePauseGate_drain shows a worker blocked on a paused gate being released
// by a graceful Stop. Waiters reports when the worker is parked, so the example
// is deterministic without relying on goroutine scheduling: Stop is called only
// after the worker is blocked, and it releases the worker with ErrDraining.
func ExamplePauseGate_drain() {
	gate := pipeline.NewPauseGate()
	gate.Pause()

	released := make(chan error, 1)
	go func() {
		released <- gate.WaitIfPaused(context.Background())
	}()

	// Wait until the worker is parked on the paused gate (no sleeps).
	for gate.Waiters() == 0 {
		runtime.Gosched()
	}
	fmt.Println("worker parked:", gate.Waiters())

	// Graceful shutdown releases the blocked worker with ErrDraining rather
	// than cancelling its context.
	gate.Stop()
	err := <-released
	fmt.Println("released with ErrDraining:", errors.Is(err, pipeline.ErrDraining))

	// Output:
	// worker parked: 1
	// released with ErrDraining: true
}

var errRateLimited = errors.New("rate limited")

// runBatch analyzes assets one at a time, skipping those already recorded in
// the checkpoint at path, and prints what happens.
func runBatch(path string, assets []string, analyze func(string) error) error {
	cp, err := pipeline.OpenFileCheckpoint(path)
	if err != nil {
		return err
	}
	process := pipeline.Checkpointed(cp, func(id string) string { return id },
		func(_ context.Context, id string) error {
			fmt.Println("analyzing", id)
			return analyze(id)
		},
		func(id string, err error) { fmt.Println("failed", id+":", err) })
	err = pipeline.ConsumeAll(context.Background(), assets, 1, process)
	return errors.Join(err, cp.Close())
}

// ExampleCheckpointed resumes a batch: the second run analyzes only the asset
// that failed in the first.
func ExampleCheckpointed() {
	dir, err := os.MkdirTemp("", "checkpoint")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "done.jsonl")
	assets := []string{"a", "b", "c"}

	fmt.Println("first run:")
	if err := runBatch(path, assets, func(id string) error {
		if id == "b" {
			return errRateLimited
		}
		return nil
	}); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("second run:")
	if err := runBatch(path, assets, func(string) error { return nil }); err != nil {
		fmt.Println(err)
	}

	// Output:
	// first run:
	// analyzing a
	// analyzing b
	// failed b: rate limited
	// analyzing c
	// second run:
	// analyzing b
}
