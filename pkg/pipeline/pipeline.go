// Package pipeline provides foundational concurrency utilities used by the
// orchestrator: a generic worker-pool consumer and a dead-letter queue
// for failed pipeline events.
package pipeline

import (
	"context"
	"sync"
)

type consumeConfig struct {
	gate *PauseGate
}

// ConsumeOption configures Consume behavior.
type ConsumeOption func(*consumeConfig)

// WithPauseGate configures Consume to check the provided PauseGate before processing each item.
func WithPauseGate(gate *PauseGate) ConsumeOption {
	return func(c *consumeConfig) {
		c.gate = gate
	}
}

// Consume abstracts the boilerplate of starting a pool of concurrent workers that
// read from an input channel, check for context cancellation, and execute a process function.
// It manages the provided WaitGroup (if not nil) to ensure all workers are cleanly awaited.
//
//nolint:revive // variadic options pattern exceeds 5-arg limit for backwards compatibility
func Consume[T any](ctx context.Context, input <-chan T, concurrency int, wg *sync.WaitGroup, process func(context.Context, T), opts ...ConsumeOption) {
	if process == nil {
		return
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	var cfg consumeConfig
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
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
					if cfg.gate != nil {
						if err := cfg.gate.WaitIfPaused(ctx); err != nil {
							return
						}
					}
					process(ctx, item)
				}
			}
		}()
	}
}
