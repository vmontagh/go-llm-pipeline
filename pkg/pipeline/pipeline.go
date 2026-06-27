// Package pipeline provides foundational concurrency utilities used by the
// orchestrator: a generic worker-pool consumer and a dead-letter queue
// for failed pipeline events.
package pipeline

import (
	"context"
	"sync"
)

// Consume abstracts the boilerplate of starting a pool of concurrent workers that
// read from an input channel, check for context cancellation, and execute a process function.
// It manages the provided WaitGroup (if not nil) to ensure all workers are cleanly awaited.
func Consume[T any](ctx context.Context, input <-chan T, concurrency int, wg *sync.WaitGroup, process func(context.Context, T)) {
	if concurrency <= 0 {
		concurrency = 1
	}
	for i := 0; i < concurrency; i++ {
		if wg != nil {
			wg.Add(1)
		}
		go func() {
			if wg != nil {
				defer wg.Done()
			}
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-input:
					if !ok {
						return
					}
					process(ctx, item)
				}
			}
		}()
	}
}
