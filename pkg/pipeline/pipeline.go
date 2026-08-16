// Package pipeline provides foundational concurrency utilities used by the
// orchestrator: a generic worker-pool consumer and a dead-letter queue
// for failed pipeline events.
//
// # Pipeline lifecycle
//
// A running pipeline can be paused, resumed, or gracefully drained without
// tearing down the request context. [PauseGate] is the choke point every
// worker passes through before processing an item, and its four states are
// distinct on purpose:
//
//   - Running: new work is accepted, in-flight work runs, ctx is live.
//   - Paused: new work blocks at the gate, in-flight work finishes then parks,
//     ctx is live.
//   - Draining: new work is refused, but in-flight and already-queued work runs
//     to completion, and ctx stays live.
//   - Cancelled: new work is refused and in-flight work is aborted because ctx
//     itself is cancelled.
//
// The important distinction is between draining and cancellation. Cancelling
// the context propagates into in-flight LLM/HTTP calls and aborts them
// mid-write, which loses the current item and prevents a clean run summary.
// Draining instead lets those calls finish normally. To keep the two apart,
// [PauseGate.WaitIfPaused] reports why a worker woke up via its return value:
//
//   - nil: proceed and process the item.
//   - [ErrDraining]: shutting down gracefully. Stop blocking on the pause, but
//     do not abort; finish queued items on the still-live ctx and return when
//     the input channel closes. errors.Is(err, context.Canceled) is false for a
//     drain, so cancellation-handling paths do not misfire.
//   - ctx error (e.g. context.Canceled): the whole request is dead; abort.
//
// [Consume] applies this contract directly: a worker parked on a paused gate
// that is then stopped is released with [ErrDraining], drains the remaining
// items, and returns cleanly once the channel closes, never cancelling ctx.
package pipeline

import (
	"context"
	"errors"
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
						err := cfg.gate.WaitIfPaused(ctx)
						switch {
						case err == nil:
							// Not paused: process the item.
						case errors.Is(err, ErrDraining):
							// Gate is draining: stop blocking on pause, but keep
							// draining already-queued items to completion on the
							// live ctx until the input channel is closed.
						default:
							// ctx cancelled: abort.
							return
						}
					}
					process(ctx, item)
				}
			}
		}()
	}
}
