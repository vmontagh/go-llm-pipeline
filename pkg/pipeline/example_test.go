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

	"golang.org/x/sync/errgroup"

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
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Println(err)
		}
	}()
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

// ExampleConsumeAll processes a slice with a pool of workers and waits for
// them. Results are sorted because workers finish in any order.
func ExampleConsumeAll() {
	var (
		mu      sync.Mutex
		lengths []int
	)
	err := pipeline.ConsumeAll(context.Background(), []string{"cat", "horse", "ox"}, 2, func(_ context.Context, s string) {
		mu.Lock()
		defer mu.Unlock()
		lengths = append(lengths, len(s))
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	sort.Ints(lengths)
	fmt.Println(lengths)

	// Output:
	// [2 3 5]
}

// Example_localAndRemote runs local, CPU-bound work and remote API calls as
// two stages, each with its own worker cap. The local stage (here, resizing
// photos) is sized to the CPU cores. The remote stage is sized to the
// provider's rate limit and uses an errgroup, so the first failed call stops
// both stages and is returned.
func Example_localAndRemote() {
	const remoteWorkers = 4 // set from the provider's rate limit
	photos := []string{"a.jpg", "b.jpg", "c.jpg"}

	g, ctx := errgroup.WithContext(context.Background())
	resized := make(chan string)
	g.Go(func() error {
		defer close(resized)
		return pipeline.ConsumeAll(ctx, photos, runtime.NumCPU(), func(ctx context.Context, p string) {
			select {
			case resized <- "small-" + p:
			case <-ctx.Done():
			}
		})
	})

	var (
		mu       sync.Mutex
		captions []string
	)
	g.Go(func() error {
		calls, ctx := errgroup.WithContext(ctx)
		calls.SetLimit(remoteWorkers)
		for {
			select {
			case p, ok := <-resized:
				if !ok {
					return calls.Wait()
				}
				calls.Go(func() error {
					c, err := describe(ctx, p)
					if err != nil {
						return fmt.Errorf("describing %s: %w", p, err)
					}
					mu.Lock()
					defer mu.Unlock()
					captions = append(captions, c)
					return nil
				})
			case <-ctx.Done():
				return calls.Wait() // a call failed; returning cancels the local stage
			}
		}
	})

	if err := g.Wait(); err != nil {
		fmt.Println(err)
		return
	}
	sort.Strings(captions)
	fmt.Println(captions)

	// Output:
	// [caption of small-a.jpg caption of small-b.jpg caption of small-c.jpg]
}

// describe stands in for a remote model call, such as Template.Analyze.
func describe(ctx context.Context, photo string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("describing photo: %w", err)
	}
	return "caption of " + photo, nil
}
