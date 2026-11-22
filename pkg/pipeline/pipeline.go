// Package pipeline provides foundational concurrency utilities used by the
// orchestrator: a generic worker-pool consumer, checkpoints that let an
// interrupted batch resume, and a dead-letter queue for failed pipeline
// events.
//
// # Resuming a batch
//
// Wrap the process function with [Checkpointed] and a [Checkpoint] such as
// [FileCheckpoint]. Completed items are recorded as they finish; a rerun with
// the same checkpoint skips them and processes only unfinished or failed
// items. Failures go to the caller's error handler, typically a
// [DeadLetterQueue] entry, and stay unmarked so the next run retries them.
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
//   - Draining: new work is refused, but in-flight and already-queued work is
//     handled per the configured [DrainMode], and ctx stays live. The default
//     [DrainComplete] runs queued work to completion; [DrainDiscard] instead
//     drops not-yet-started items via a drain handler.
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
// that is then stopped is released with [ErrDraining]. Under the default
// [DrainComplete] it drains the remaining items and returns cleanly once the
// channel closes; under [DrainDiscard] it drops not-yet-started items via the
// configured drain handler and returns once the channel closes. Neither path
// cancels ctx.
package pipeline

import (
	"context"
	"errors"
	"sync"
)

// DrainMode selects how Consume handles items once the pause gate is draining
// (after [PauseGate.Stop]). The zero value is the backward-compatible default.
type DrainMode int

const (
	// DrainComplete finishes queued and in-flight items on the live ctx until
	// the input channel closes. It is the default drain behavior.
	DrainComplete DrainMode = iota
	// DrainDiscard stops intake: not-yet-started items are dropped instead of
	// processed, accounted for via the configured drain handler.
	DrainDiscard
)

type consumeConfig struct {
	gate         *PauseGate
	drainMode    DrainMode
	drainDiscard func(any)
}

// ConsumeOption configures Consume behavior.
type ConsumeOption func(*consumeConfig)

// WithPauseGate configures Consume to check the provided PauseGate before processing each item.
func WithPauseGate(gate *PauseGate) ConsumeOption {
	return func(c *consumeConfig) {
		c.gate = gate
	}
}

// WithDrainMode configures how Consume handles items once the gate is draining.
func WithDrainMode(m DrainMode) ConsumeOption {
	return func(c *consumeConfig) {
		c.drainMode = m
	}
}

// WithDrainHandler accounts for each item Consume discards under DrainDiscard
// (e.g. stats, WaitGroup balancing, skipped events). T is inferred from fn. A
// nil fn is a no-op.
func WithDrainHandler[T any](fn func(T)) ConsumeOption {
	return func(c *consumeConfig) {
		if fn != nil {
			c.drainDiscard = func(v any) {
				if t, ok := v.(T); ok {
					fn(t)
				}
			}
		}
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
							// Gate is draining. Under DrainDiscard, drop this
							// not-yet-started item via the handler and keep
							// pulling to empty the channel until it closes.
							// Under DrainComplete, fall through to process it on
							// the live ctx until the input channel is closed.
							if cfg.drainMode == DrainDiscard {
								if cfg.drainDiscard != nil {
									cfg.drainDiscard(item)
								}
								continue
							}
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
