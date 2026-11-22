package analyzer

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v4"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// Builder is the single entry point that assembles an Analyzer around a Provider.
type Builder[T any, R any] struct {
	provider  provider.Provider[T]
	parser    func(string) (R, error)
	opts      []TemplateConfigurator
	guard     bool
	guardOpts []provider.GuardOption
}

// NewBuilder returns a Builder that will assemble an Analyzer around p.
func NewBuilder[T any, R any](p provider.Provider[T], parser func(string) (R, error)) *Builder[T, R] {
	return &Builder[T, R]{provider: p, parser: parser}
}

// WithLogger sets the *slog.Logger forwarded to the Template.
func (b *Builder[T, R]) WithLogger(l *slog.Logger) *Builder[T, R] {
	if l != nil {
		b.opts = append(b.opts, WithTemplateLogger(l))
	}
	return b
}

// WithTracer sets the tracer used to wrap Analyze and HandleProviderError.
func (b *Builder[T, R]) WithTracer(t Tracer) *Builder[T, R] {
	if t != nil {
		b.opts = append(b.opts, WithTemplateTracer(t))
	}
	return b
}

// WithRetryPolicy overrides the per-error-class retry policy used by the Template.
func (b *Builder[T, R]) WithRetryPolicy(p RetryPolicy) *Builder[T, R] {
	if p.Tiers != nil {
		b.opts = append(b.opts, WithTemplateRetryPolicy(p))
	}
	return b
}

// WithMaxElapsedTime sets the L1 backoff budget.
func (b *Builder[T, R]) WithMaxElapsedTime(d time.Duration) *Builder[T, R] {
	if d != 0 {
		b.opts = append(b.opts, WithTemplateMaxElapsedTime(d))
	}
	return b
}

// WithMaxParseRetries caps how many times the Template re-asks the provider after a parse failure.
func (b *Builder[T, R]) WithMaxParseRetries(r int) *Builder[T, R] {
	b.opts = append(b.opts, WithTemplateMaxParseRetries(r))
	return b
}

// WithBackOff injects a custom BackOff strategy.
func (b *Builder[T, R]) WithBackOff(bo backoff.BackOff) *Builder[T, R] {
	if bo != nil {
		b.opts = append(b.opts, WithTemplateBackOff(bo))
	}
	return b
}

// WithShortCooldownRetryThreshold sets the maximum ErrAllKeysCoolingDown wait before L2 escalation.
func (b *Builder[T, R]) WithShortCooldownRetryThreshold(d time.Duration) *Builder[T, R] {
	b.opts = append(b.opts, WithTemplateShortCooldownRetryThreshold(d))
	return b
}

// WithMaxAssetBytes bounds how many bytes AnalyzeAsset reads from an Asset
// payload. Zero disables the bound; negative values are ignored.
func (b *Builder[T, R]) WithMaxAssetBytes(n int64) *Builder[T, R] {
	b.opts = append(b.opts, WithTemplateMaxAssetBytes(n))
	return b
}

// WithGuard wraps the Provider in a provider.Guard (adaptive concurrency and
// circuit breaker) at Build.
func (b *Builder[T, R]) WithGuard(opts ...provider.GuardOption) *Builder[T, R] {
	b.guard = true
	b.guardOpts = opts
	return b
}

// Build assembles the Template around the configured Provider and returns the concrete *Template[T, R].
func (b *Builder[T, R]) Build() (*Template[T, R], error) {
	p := b.provider
	if b.guard && p != nil {
		g, err := provider.NewGuard(p, b.guardOpts...)
		if err != nil {
			return nil, fmt.Errorf("building guard: %w", err)
		}
		p = g
	}
	return NewTemplate[T, R](p, b.parser, b.opts...)
}

// MustBuild assembles the Template around the configured Provider and panics if it fails.
func (b *Builder[T, R]) MustBuild() *Template[T, R] {
	t, err := b.Build()
	if err != nil {
		panic("analyzer: MustBuild failed: " + err.Error())
	}
	return t
}
