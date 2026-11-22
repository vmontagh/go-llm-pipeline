package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// ErrInvalidGuardConfig is returned by NewGuard for a nil provider or out-of-range options.
var ErrInvalidGuardConfig = errors.New("invalid guard config")

// Metric names a Guard adds to its Adder.
const (
	MetricGuardWaits          = "guard_waits"
	MetricGuardLimitDecreases = "guard_limit_decreases"
	MetricGuardBreakerOpens   = "guard_breaker_opens"
)

const (
	defaultGuardMinLimit    = 1
	defaultGuardMaxLimit    = 16
	defaultBreakerFailures  = 5
	defaultBreakerOpenFor   = 30 * time.Second
	rateLimitDecreaseFactor = 0.5
	latencyDecreaseFactor   = 0.9
)

// BreakerState is the circuit breaker state of a Guard.
type BreakerState int

// Breaker states. The zero value is BreakerUnknown.
const (
	BreakerUnknown BreakerState = iota
	// BreakerClosed lets calls through.
	BreakerClosed
	// BreakerOpen holds calls until the open window ends.
	BreakerOpen
	// BreakerHalfOpen lets one probe call through; the others wait for its result.
	BreakerHalfOpen
)

// String returns the state name.
func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// GuardClock supplies time and timers to a Guard; tests inject a fake.
type GuardClock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realGuardClock struct{}

func (realGuardClock) Now() time.Time                         { return time.Now() }
func (realGuardClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Adder receives counter increments; *observ.Metrics implements it.
type Adder interface {
	Add(name string, delta int64)
}

type guardConfig struct {
	minLimit        int
	maxLimit        int
	latencyTarget   time.Duration
	breakerFailures int
	breakerOpenFor  time.Duration
	clock           GuardClock
	metrics         Adder
}

// GuardOption configures a Guard.
type GuardOption func(*guardConfig)

// WithConcurrencyLimits bounds the adaptive in-flight limit. The limit starts at minLimit.
func WithConcurrencyLimits(minLimit, maxLimit int) GuardOption {
	return func(c *guardConfig) { c.minLimit, c.maxLimit = minLimit, maxLimit }
}

// WithLatencyTarget shrinks the limit when a successful call takes longer than d. Zero disables it.
func WithLatencyTarget(d time.Duration) GuardOption {
	return func(c *guardConfig) { c.latencyTarget = d }
}

// WithBreaker opens the breaker after failures consecutive transient errors, for openFor
// or the provider's Retry-After, whichever is longer.
func WithBreaker(failures int, openFor time.Duration) GuardOption {
	return func(c *guardConfig) { c.breakerFailures, c.breakerOpenFor = failures, openFor }
}

// WithGuardClock replaces the real clock.
func WithGuardClock(clock GuardClock) GuardOption {
	return func(c *guardConfig) { c.clock = clock }
}

// WithGuardMetrics sends the Metric* counters to a.
func WithGuardMetrics(a Adder) GuardOption {
	return func(c *guardConfig) { c.metrics = a }
}

// Guard wraps a Provider with an adaptive concurrency limit and a circuit breaker.
//
// The limit grows by one slot per limit successes and halves on rate limits
// (HTTP 429, Retry-After, or TierShortRateLimit). Calls over the limit wait.
// Consecutive transient errors open the breaker; calls then wait until it
// half-opens and one probe call succeeds.
type Guard[T any] struct {
	Provider[T]

	cfg guardConfig

	mu        sync.Mutex
	limit     float64
	inFlight  int
	state     BreakerState
	failures  int
	openUntil time.Time
	probing   bool
	// changed is closed and replaced whenever a waiter may proceed.
	changed chan struct{}
}

// NewGuard wraps p. Defaults: limits 1..16, breaker after 5 failures for 30s, no latency target.
func NewGuard[T any](p Provider[T], opts ...GuardOption) (*Guard[T], error) {
	cfg := guardConfig{
		minLimit:        defaultGuardMinLimit,
		maxLimit:        defaultGuardMaxLimit,
		breakerFailures: defaultBreakerFailures,
		breakerOpenFor:  defaultBreakerOpenFor,
		clock:           realGuardClock{},
	}
	for _, o := range opts {
		o(&cfg)
	}
	switch {
	case p == nil:
		return nil, fmt.Errorf("%w: nil provider", ErrInvalidGuardConfig)
	case cfg.minLimit < 1 || cfg.maxLimit < cfg.minLimit:
		return nil, fmt.Errorf("%w: limits %d..%d", ErrInvalidGuardConfig, cfg.minLimit, cfg.maxLimit)
	case cfg.breakerFailures < 1 || cfg.breakerOpenFor <= 0:
		return nil, fmt.Errorf("%w: breaker %d failures for %v", ErrInvalidGuardConfig, cfg.breakerFailures, cfg.breakerOpenFor)
	case cfg.latencyTarget < 0:
		return nil, fmt.Errorf("%w: latency target %v", ErrInvalidGuardConfig, cfg.latencyTarget)
	case cfg.clock == nil:
		return nil, fmt.Errorf("%w: nil clock", ErrInvalidGuardConfig)
	}
	return &Guard[T]{
		Provider: p,
		cfg:      cfg,
		limit:    float64(cfg.minLimit),
		state:    BreakerClosed,
		changed:  make(chan struct{}),
	}, nil
}

// Limit returns the current in-flight limit.
func (g *Guard[T]) Limit() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return int(g.limit)
}

// State returns the breaker state.
func (g *Guard[T]) State() BreakerState {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == BreakerOpen && !g.cfg.clock.Now().Before(g.openUntil) {
		return BreakerHalfOpen
	}
	return g.state
}

// Call waits for a slot and an allowing breaker, then calls the wrapped provider.
// If ctx ends while waiting, it returns an OutcomeTerminal result.
func (g *Guard[T]) Call(ctx context.Context, lease *Lease[T], prompt string, imageData []byte) CallResult {
	probe, err := g.acquire(ctx)
	if err != nil {
		return CallResult{Err: backoff.Permanent(fmt.Errorf("waiting for guard: %w", err)), Outcome: OutcomeTerminal}
	}
	start := g.cfg.clock.Now()
	res := g.Provider.Call(ctx, lease, prompt, imageData)
	g.release(&res, probe, g.cfg.clock.Now().Sub(start))
	return res
}

// acquire blocks until the call may run and reports whether it is the half-open probe.
func (g *Guard[T]) acquire(ctx context.Context) (bool, error) {
	waited := false
	for {
		g.mu.Lock()
		now := g.cfg.clock.Now()
		if g.state == BreakerOpen && !now.Before(g.openUntil) {
			g.state = BreakerHalfOpen
		}
		var timer <-chan time.Time
		switch {
		case g.state == BreakerOpen:
			timer = g.cfg.clock.After(g.openUntil.Sub(now))
		case g.inFlight >= int(g.limit), g.state == BreakerHalfOpen && g.probing:
			// Wait for a release.
		default:
			probe := g.state == BreakerHalfOpen
			g.probing = g.probing || probe
			g.inFlight++
			g.mu.Unlock()
			return probe, nil
		}
		changed := g.changed
		g.mu.Unlock()
		if !waited {
			waited = true
			g.add(MetricGuardWaits)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-changed:
		case <-timer:
		}
	}
}

// release frees the slot and feeds the result to the limit and the breaker.
func (g *Guard[T]) release(res *CallResult, probe bool, latency time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inFlight--
	if probe {
		g.probing = false
	}
	switch res.Outcome {
	case OutcomeSuccess:
		g.onSuccess(probe, latency)
	case OutcomeProviderError:
		g.onTransient(res, probe)
	}
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *Guard[T]) onSuccess(probe bool, latency time.Duration) {
	g.failures = 0
	if probe {
		g.state = BreakerClosed
	}
	if g.cfg.latencyTarget > 0 && latency > g.cfg.latencyTarget {
		g.decrease(latencyDecreaseFactor)
		return
	}
	g.limit = math.Min(g.limit+1/g.limit, float64(g.cfg.maxLimit))
}

func (g *Guard[T]) onTransient(res *CallResult, probe bool) {
	tier := g.ClassifyError(ClassifyInput{Err: res.Err, StatusCode: res.StatusCode, Body: res.Body, RetryAfter: res.RetryAfter}, RetryContext{})
	if tier == TierTerminal {
		return
	}
	if res.StatusCode == http.StatusTooManyRequests || res.RetryAfter > 0 || tier == TierShortRateLimit {
		g.decrease(rateLimitDecreaseFactor)
	}
	g.failures++
	if probe || (g.state == BreakerClosed && g.failures >= g.cfg.breakerFailures) {
		g.state = BreakerOpen
		g.openUntil = g.cfg.clock.Now().Add(max(g.cfg.breakerOpenFor, res.RetryAfter))
		g.add(MetricGuardBreakerOpens)
	}
}

func (g *Guard[T]) decrease(factor float64) {
	next := math.Max(g.limit*factor, float64(g.cfg.minLimit))
	if int(next) < int(g.limit) {
		g.add(MetricGuardLimitDecreases)
	}
	g.limit = next
}

func (g *Guard[T]) add(name string) {
	if g.cfg.metrics != nil {
		g.cfg.metrics.Add(name, 1)
	}
}
