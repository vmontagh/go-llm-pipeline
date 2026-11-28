package analyzer

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// Builder is the single entry point that assembles a Template around a Provider.
type Builder[T any, R any] struct {
	provider  provider.Provider[T]
	parser    func(string) (R, error)
	cfg       templateConfig
	guard     bool
	guardOpts []provider.GuardOption
}

// NewBuilder returns a Builder that will assemble a Template around p, parsing
// each reply with parser.
func NewBuilder[T any, R any](p provider.Provider[T], parser func(string) (R, error)) *Builder[T, R] {
	return &Builder[T, R]{provider: p, parser: parser, cfg: defaultTemplateConfig()}
}

// WithLogger sets the logger for debug and warning messages. A nil logger is ignored.
func (b *Builder[T, R]) WithLogger(l *slog.Logger) *Builder[T, R] {
	if l != nil {
		b.cfg.logger = l
	}
	return b
}

// WithTracer sets the tracer used to wrap Analyze and provider error handling.
// A nil tracer is ignored.
func (b *Builder[T, R]) WithTracer(t Tracer) *Builder[T, R] {
	if t != nil {
		b.cfg.tracer = t
	}
	return b
}

// WithRetryPolicy overrides the per-error-class retry policy. A policy with
// nil Tiers is ignored.
func (b *Builder[T, R]) WithRetryPolicy(p policy.RetryPolicy) *Builder[T, R] {
	if p.Tiers != nil {
		b.cfg.retryPolicy = p
	}
	return b
}

// WithMaxElapsedTime sets the total backoff budget for one call. Zero keeps the default.
func (b *Builder[T, R]) WithMaxElapsedTime(d time.Duration) *Builder[T, R] {
	if d != 0 {
		b.cfg.maxElapsedTime = d
	}
	return b
}

// WithMaxParseRetries caps how many times the Template re-asks the provider after a parse failure.
func (b *Builder[T, R]) WithMaxParseRetries(r int) *Builder[T, R] {
	b.cfg.maxParseRetries = r
	return b
}

// WithBackOff injects a custom BackOff strategy. A nil BackOff is ignored.
func (b *Builder[T, R]) WithBackOff(bo backoff.BackOff) *Builder[T, R] {
	if bo != nil {
		b.cfg.backOff = bo
	}
	return b
}

// WithShortCooldownRetryThreshold sets the longest provider.ErrAllKeysCoolingDown
// wait that is retried in place before escalating.
func (b *Builder[T, R]) WithShortCooldownRetryThreshold(d time.Duration) *Builder[T, R] {
	b.cfg.shortCooldown = d
	return b
}

// WithScrubFunc sets a function applied to prompt and response text before
// they are logged at debug level. Use it to redact PII or secrets. It never
// changes what is sent to the provider or passed to the parser. A nil
// function is ignored.
func (b *Builder[T, R]) WithScrubFunc(fn func(string) string) *Builder[T, R] {
	if fn != nil {
		b.cfg.scrubFn = fn
	}
	return b
}

// WithMaxAssetBytes bounds how many bytes AnalyzeAsset reads from an Asset
// payload. Zero disables the bound; negative values are ignored. Defaults to
// DefaultMaxAssetBytes.
func (b *Builder[T, R]) WithMaxAssetBytes(n int64) *Builder[T, R] {
	if n >= 0 {
		b.cfg.maxAssetBytes = n
	}
	return b
}

// WithGuard wraps the Provider in a provider.Guard (adaptive concurrency and
// circuit breaker) at Build.
func (b *Builder[T, R]) WithGuard(opts ...provider.GuardOption) *Builder[T, R] {
	b.guard = true
	b.guardOpts = opts
	return b
}

// Build assembles the Template. It returns ErrNilProvider or ErrNilParser when
// either is missing, and an error if the guard configuration is invalid.
func (b *Builder[T, R]) Build() (*Template[T, R], error) {
	p := b.provider
	if b.guard && p != nil {
		g, err := provider.NewGuard(p, b.guardOpts...)
		if err != nil {
			return nil, fmt.Errorf("building guard: %w", err)
		}
		p = g
	}
	return newTemplate(p, b.parser, &b.cfg)
}
