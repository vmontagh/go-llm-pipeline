package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// runBatch processes ids once, resuming from the checkpoint in dir and
// sending failures to the dead letter queue there. fail names an asset that
// fails in this run.
func runBatch(dir, fail string, ids []string) (processed []string, done int, err error) {
	cp, err := pipeline.OpenFileCheckpoint(filepath.Join(dir, "done.jsonl"))
	if err != nil {
		return nil, 0, err
	}
	defer cp.Close()
	dlq, err := pipeline.NewDeadLetterQueue(filepath.Join(dir, "dlq.jsonl"), nil)
	if err != nil {
		return nil, 0, err
	}
	defer dlq.Close()

	var mu sync.Mutex
	analyze := func(_ context.Context, id string) error {
		mu.Lock()
		defer mu.Unlock()
		processed = append(processed, id)
		if id == fail {
			return errRateLimited
		}
		return nil
	}
	recordFailure := func(id string, err error) {
		if err := dlq.Add(&pipeline.DLQEntry{ItemID: id, Stage: "analyze", Error: err.Error(), Attempts: 1}); err != nil {
			log.Print(err)
		}
	}

	input := make(chan string)
	var wg sync.WaitGroup
	pipeline.Consume(context.Background(), input, 2, &wg,
		pipeline.Checkpointed(cp, func(id string) string { return id }, analyze, recordFailure))
	for _, id := range ids {
		input <- id
	}
	close(input)
	wg.Wait()
	sort.Strings(processed)
	return processed, cp.Len(), nil
}

// ExampleCheckpointed shows a batch that resumes after an interruption: the
// second run skips the assets the first run completed and retries only the
// failure, which the first run also sent to the dead letter queue.
func ExampleCheckpointed() {
	dir, err := os.MkdirTemp("", "checkpoint")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer os.RemoveAll(dir)
	ids := []string{"a", "b", "c"}

	for _, fail := range []string{"b", ""} {
		processed, done, err := runBatch(dir, fail, ids)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println("processed:", processed, "done:", done)
	}

	f, err := os.Open(filepath.Join(dir, "dlq.jsonl"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer f.Close()
	failed, err := pipeline.ReadDLQ(f)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("dead letters:", failed[0].ItemID, failed[0].Error)

	// Output:
	// processed: [a b c] done: 2
	// processed: [b] done: 3
	// dead letters: b rate limited
}
