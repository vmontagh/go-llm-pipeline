// Package provider — keypool.go implements KeyPool, the concurrency-safe
// pool of API credentials with per-key cooldown semantics that the
// per-backend analyzer providers (openai, gemini, ollama) use to rotate
// keys on quota / rate-limit errors.
package provider

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PoolClock is the small subset of the standard time package that
// KeyPool depends on. It exists so tests can drive the pool with
// virtual time and not depend on wall-clock sleeps.
type PoolClock interface {
	Now() time.Time
}

// RealPoolClock is the wall-clock PoolClock implementation used in
// production.
type RealPoolClock struct{}

// Now returns the current wall-clock time.
func (RealPoolClock) Now() time.Time { return time.Now() }

// keySlot tracks the per-key state inside a KeyPool.
type keySlot[T any] struct {
	value      T
	idx        int
	cooldownTo time.Time
	lastUsed   time.Time
}

// KeyPool is a concurrency-safe pool of API credentials (or any value
// type T) with per-key cooldown semantics.
type KeyPool[T any] struct {
	mu    sync.Mutex
	slots []*keySlot[T]
	clock PoolClock
}

// NewKeyPool returns a pool over items. items is copied internally so
// the caller may reuse the slice.
func NewKeyPool[T any](items []T) *KeyPool[T] {
	return NewKeyPoolWithClock(items, RealPoolClock{})
}

// NewKeyPoolWithClock is the test-friendly constructor accepting an
// injectable clock.
func NewKeyPoolWithClock[T any](items []T, clk PoolClock) *KeyPool[T] {
	p := &KeyPool[T]{
		slots: make([]*keySlot[T], len(items)),
		clock: clk,
	}
	for i, it := range items {
		p.slots[i] = &keySlot[T]{value: it, idx: i}
	}
	return p
}

// Len returns the total number of keys configured.
func (p *KeyPool[T]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.slots)
}

// Available returns the number of keys whose cooldown has elapsed.
func (p *KeyPool[T]) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	n := 0
	for _, s := range p.slots {
		if s.cooldownTo.IsZero() || !s.cooldownTo.After(now) {
			n++
		}
	}
	return n
}

// Lease is the handle returned by Acquire. The caller must eventually
// pass it to exactly one of Release / MarkCooldown.
type Lease[T any] struct {
	Value T
	Index int
	pool  *KeyPool[T]
	slot  *keySlot[T]
}

// Acquire returns a Lease on an available key without blocking.
//
//   - Empty pool: returns ErrMissingAPIKey.
//   - All slots cooling: returns ErrAllKeysCoolingDown.
//   - ctx already cancelled: returns ctx.Err().
func (p *KeyPool[T]) Acquire(ctx context.Context) (*Lease[T], error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("KeyPool.Acquire: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.slots) == 0 {
		return nil, ErrMissingAPIKey
	}

	now := p.clock.Now()
	best := p.pickAvailableLocked(now)
	if best == nil {
		return nil, ErrAllKeysCoolingDown
	}
	best.lastUsed = now
	return &Lease[T]{
		Value: best.value,
		Index: best.idx,
		pool:  p,
		slot:  best,
	}, nil
}

// pickAvailableLocked returns the best slot to lease, or nil when
// every slot is cooling. The caller must hold p.mu.
func (p *KeyPool[T]) pickAvailableLocked(now time.Time) *keySlot[T] {
	var best *keySlot[T]
	for _, s := range p.slots {
		if !s.cooldownTo.IsZero() && s.cooldownTo.After(now) {
			continue
		}
		s.cooldownTo = time.Time{}
		if best == nil || s.lastUsed.Before(best.lastUsed) {
			best = s
		}
	}
	return best
}

// MinCooldown returns the minimum duration until at least one key is ready.
// If a key is currently ready, it returns 0. If the pool is empty, it returns 0.
func (p *KeyPool[T]) MinCooldown() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.slots) == 0 {
		return 0
	}

	now := p.clock.Now()
	var minWait time.Duration = -1

	for _, s := range p.slots {
		if !s.cooldownTo.IsZero() && s.cooldownTo.After(now) {
			wait := s.cooldownTo.Sub(now)
			if minWait == -1 || wait < minWait {
				minWait = wait
			}
			continue
		}
		// Found a ready key
		return 0
	}

	if minWait == -1 {
		return 0
	}
	return minWait
}

// Release marks the lease's slot as successfully used (no cooldown)
// and frees it for future Acquire calls. Safe to call more than once.
func (l *Lease[T]) Release() {
	if l == nil || l.pool == nil {
		return
	}
	l.pool.releaseSlot(l.slot, 0)
}

// MarkCooldown puts the lease's slot in cooldown for the duration
// specified by the policy, with retryAfter optionally acting as a
// floor when policy.HonorRetryAfter is true. Negative or zero
// policy.Cooldown values release the slot immediately.
func (l *Lease[T]) MarkCooldown(policy TierPolicy, retryAfter time.Duration) {
	if l == nil || l.pool == nil {
		return
	}
	cd := policy.Cooldown
	if policy.HonorRetryAfter && retryAfter > cd {
		cd = retryAfter
	}
	l.pool.releaseSlot(l.slot, cd)
}

// releaseSlot is the single mutating helper used by every Lease.* call.
func (p *KeyPool[T]) releaseSlot(s *keySlot[T], cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	s.lastUsed = now
	s.cooldownTo = time.Time{}
	if cooldown > 0 {
		s.cooldownTo = now.Add(cooldown)
	}
}
