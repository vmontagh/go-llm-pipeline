package pipeline

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConsume_NilProcess(t *testing.T) {
	t.Parallel()
	input := make(chan int)
	Consume(t.Context(), input, 1, nil, nil)
}

func TestConsume_Success(t *testing.T) {
	t.Parallel()
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
}

func TestConsume_WithPauseGate_BlocksNewWork(t *testing.T) {
	t.Parallel()
	gate := NewPauseGate()
	gate.Pause()

	input := make(chan int, 2)
	input <- 1
	input <- 2
	close(input)

	var count atomic.Int64
	var wg sync.WaitGroup

	process := func(_ context.Context, _ int) {
		count.Add(1)
	}

	Consume(t.Context(), input, 2, &wg, process, WithPauseGate(gate))

	// Give workers a moment to try picking up work
	time.Sleep(50 * time.Millisecond)

	if got := count.Load(); got != 0 {
		t.Fatalf("count before resume = %d, want 0", got)
	}

	gate.Resume()
	wg.Wait()

	if got := count.Load(); got != 2 {
		t.Fatalf("count after resume = %d, want 2", got)
	}
}

func TestConsume_PauseMidStream(t *testing.T) {
	t.Parallel()
	gate := NewPauseGate()
	input := make(chan int, 5)

	var count atomic.Int64
	var wg sync.WaitGroup
	inFlightStarted := make(chan struct{})
	continueInFlight := make(chan struct{})

	process := func(_ context.Context, item int) {
		if item == 1 {
			close(inFlightStarted)
			<-continueInFlight
		}
		count.Add(1)
	}

	Consume(t.Context(), input, 1, &wg, process, WithPauseGate(gate))

	input <- 1
	<-inFlightStarted

	// Item 1 is running; pause gate now and send item 2
	gate.Pause()
	input <- 2
	close(input)

	// Allow item 1 to finish
	close(continueInFlight)

	time.Sleep(50 * time.Millisecond)

	if got := count.Load(); got != 1 {
		t.Fatalf("count while paused = %d, want 1", got)
	}

	gate.Resume()
	wg.Wait()

	if got := count.Load(); got != 2 {
		t.Fatalf("count after resume = %d, want 2", got)
	}
}

func TestConsume_NilGateIsNoop(t *testing.T) {
	t.Parallel()
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
}
