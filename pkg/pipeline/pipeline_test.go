package pipeline

import (
	"context"
	"sync"
	"testing"
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
