// Package policy holds the foundational, provider-agnostic retry/error
// taxonomy shared by every layer that needs to make routing decisions
// on a failure: the analyzer adapters, the orchestrator/tagger pipeline
// and the CLI composition root. Pulling these types out of pkg/analyzer
// breaks the latent "tagger -> analyzer" coupling — the tagger's L2 queue
// retry loop now talks to a vocabulary that does not name a specific LLM
// provider.
//
// The package owns:
//
//   - The ErrorTier and TierPolicy types (type-aliased to the provider
//     package so the wire types remain shared with the low-level KeyPool
//     cooldown machinery without forcing dependents to import provider).
//   - The RetryPolicy struct plus its Lookup/Default helpers.
//   - The Code / WithCode / IsRetryable / IsLongRetryable error
//     classification primitives, including the canonical Tier<->Code
//     round-trip used by every retry tier.
//
// Provider sentinel errors (ErrQuotaExceeded, ErrRateLimitExceeded, ...)
// remain in pkg/provider; analyzer-specific sentinels
// (ErrParsing, ErrMalformedResponse, ErrRetryableResponse) stay in
// pkg/analyzer because they describe analyzer-internal parsing
// failures rather than a retry routing class.
package policy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"google.golang.org/grpc/codes"
)

// ErrorTier is the actionable retry category for a failure. It is
// type-aliased to provider.ErrorTier so that the analyzer providers
// and the orchestrator share exactly the same value with no conversion
// at the boundary.
type ErrorTier = provider.ErrorTier

// TierPolicy mirrors provider.TierPolicy so callers can configure cooldowns
// and L2 retry budgets without importing the low-level provider package.
type TierPolicy = provider.TierPolicy

// Tier constants re-exported from provider. Because ErrorTier is a type
// alias, these constants are exactly the same values used by the
// provider's KeyPool — no conversion needed at any call site.
const (
	TierTerminal              = provider.TierTerminal
	TierShortTransient        = provider.TierShortTransient
	TierShortRateLimit        = provider.TierShortRateLimit
	TierLongTransientBounded  = provider.TierLongTransientBounded
	TierLongTransientInfinite = provider.TierLongTransientInfinite
)

// RetryPolicy is the central, per-ErrorTier configuration map for the
// analyzer + orchestrator retry stack. Operators can override any
// individual tier via config; tiers that are not listed inherit the
// zero TierPolicy from Lookup ("no cooldown, no extra L2 budget"),
// ensuring that adding a new tier to the codebase without updating
// config does not silently turn into infinite retries.
type RetryPolicy struct {
	Tiers map[ErrorTier]TierPolicy
}

// Lookup returns the policy for tier, falling back to a conservative
// zero TierPolicy for tiers the operator did not explicitly configure.
func (p RetryPolicy) Lookup(tier ErrorTier) TierPolicy {
	if p.Tiers != nil {
		if tp, ok := p.Tiers[tier]; ok {
			return tp
		}
	}
	return TierPolicy{}
}

// DefaultCodeToTier returns the canonical mapping from a gRPC code to
// its default ErrorTier. Per-analyzer ClassifyError implementations may
// route differently, but this default is used by callers that only have
// an error in hand (e.g. the orchestrator routing a failed job through
// L2 from the analyzer's wrapped, code-tagged error).
func DefaultCodeToTier(code codes.Code) ErrorTier {
	switch code {
	case codes.ResourceExhausted:
		return TierLongTransientInfinite
	case codes.FailedPrecondition:
		return TierLongTransientBounded
	case codes.Unavailable:
		return TierShortTransient
	default:
		return TierTerminal
	}
}

// DefaultRetryPolicy returns the policy used when the operator has not
// supplied one. The values reflect the four-tier retry model:
//
//   - L1 (in-call backoff): TierShortTransient / TierShortRateLimit are
//     handled by the analyzer's own backoff loop.
//   - L2.2 (infinite queue retry): TierLongTransientInfinite — temporary
//     429 throttles always clear; the job is never discarded.
//   - L2.1 (bounded queue retry): TierLongTransientBounded — hard quota
//     exhaustion may reset at the next billing window; L2MaxRetries caps
//     the attempts so we do not spin forever on a permanently overdrawn
//     account.
//   - L3 (DLQ): TierTerminal — Cooldown is still applied so a poisoned
//     credential (e.g. revoked API key) doesn't get re-acquired
//     immediately.
//
// Callers that want to tune any individual tier should start from
// DefaultRetryPolicy() and overwrite specific entries.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		Tiers: map[ErrorTier]TierPolicy{
			TierLongTransientInfinite: {
				Cooldown:        5 * time.Minute,
				HonorRetryAfter: true,
				L2BaseBackoff:   60 * time.Second,
				L2MaxBackoff:    15 * time.Minute,
			},
			TierLongTransientBounded: {
				Cooldown:              1 * time.Hour,
				HonorRetryAfter:       false,
				L2MaxRetries:          5,
				L2BaseBackoff:         10 * time.Minute,
				L2MaxBackoff:          1 * time.Hour,
				EscalateAfterAttempts: 5,
			},
			TierShortRateLimit: {
				Cooldown:              5 * time.Minute,
				HonorRetryAfter:       true,
				EscalateAfterAttempts: 3,
			},
			TierShortTransient: {
				Cooldown:              2 * time.Second,
				HonorRetryAfter:       true,
				EscalateAfterAttempts: 5,
			},
			TierTerminal: {
				Cooldown:        30 * time.Minute,
				HonorRetryAfter: false,
			},
		},
	}
}

// TierCode maps an ErrorTier to the canonical gRPC status code used for
// RetryPolicy.Lookup and WithCode wrapping. For TierTerminal it falls
// back to Code(err) to preserve specific terminal codes embedded in the
// error (e.g. Unauthenticated for a missing API key, InvalidArgument for
// parse failures) rather than collapsing every DLQ error to a single
// placeholder code.
func TierCode(tier ErrorTier, err error) codes.Code {
	switch tier {
	case TierShortTransient:
		return codes.Unavailable
	case TierShortRateLimit:
		return codes.ResourceExhausted
	case TierLongTransientBounded:
		return codes.FailedPrecondition
	case TierLongTransientInfinite:
		return codes.ResourceExhausted
	default:
		return Code(err)
	}
}

// Code returns the canonical gRPC status code for err.
//
// The lookup walks the wrapped chain via errors.As, looking for a value
// produced by WithCode. Unknown errors map to codes.Internal so callers
// always have a code to switch on; nil maps to codes.OK.
//
// As a fallback, the provider sentinel errors are mapped directly so
// that call sites which wrap a provider sentinel without going through
// WithCode still classify correctly. Analyzer-specific sentinels
// (ErrParsing, ErrMalformedResponse, ErrRetryableResponse) are not
// inspected here — those errors are always wrapped via WithCode by the
// analyzer before they escape the package, so the codedError path
// already classifies them.
func Code(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if errors.Is(err, context.Canceled) {
		return codes.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return codes.DeadlineExceeded
	}

	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code
	}

	switch {
	case errors.Is(err, provider.ErrQuotaExceeded):
		return codes.FailedPrecondition
	case errors.Is(err, provider.ErrRateLimitExceeded):
		return codes.ResourceExhausted
	case errors.Is(err, provider.ErrAllKeysCoolingDown):
		return codes.ResourceExhausted
	case errors.Is(err, provider.ErrMissingAPIKey):
		return codes.Unauthenticated
	case errors.Is(err, provider.ErrEmptyCandidate),
		errors.Is(err, provider.ErrInvalidResponsePart):
		return codes.InvalidArgument
	case errors.Is(err, provider.ErrUnexpectedStatus):
		return codes.Unavailable
	}
	return codes.Internal
}

// IsRetryable reports whether err falls into a class that some retry
// loop (L1 or L2) is expected to handle. It is the package's single
// source of truth for "should we try again?".
func IsRetryable(err error) bool {
	switch Code(err) {
	case codes.ResourceExhausted, codes.Unavailable, codes.FailedPrecondition:
		return true
	default:
		return false
	}
}

// IsLongRetryable reports whether err belongs to a quota or rate-limit
// class that warrants the longer L2 retry schedule. It is equivalent to
// IsRetryable(err) && isQuotaClass(err) and is what callers use to
// decide between the short (transient) and long (quota) retry paths.
func IsLongRetryable(err error) bool {
	return IsRetryable(err) && isQuotaClass(err)
}

// isQuotaClass reports whether err is in a quota or rate-limit family
// (ResourceExhausted for temporary throttles, FailedPrecondition for
// hard quota exhaustion).
func isQuotaClass(err error) bool {
	c := Code(err)
	return c == codes.ResourceExhausted || c == codes.FailedPrecondition
}

// codedError tags an underlying error with a canonical gRPC code so
// that downstream code can branch on it without string-matching. The
// type is intentionally unexported: callers should produce values only
// via WithCode and consume them only via Code / IsRetryable /
// IsLongRetryable. This keeps the contract narrow.
type codedError struct {
	code codes.Code
	err  error
}

func (e *codedError) Error() string {
	if e.err == nil {
		return e.code.String()
	}
	return fmt.Sprintf("%s: %s", e.code.String(), e.err.Error())
}

// Unwrap allows errors.Is / errors.As to traverse the chain.
func (e *codedError) Unwrap() error { return e.err }

// Is supports errors.Is comparison against another *codedError that has
// the same code, even if their inner errors differ.
func (e *codedError) Is(target error) bool {
	var t *codedError
	if errors.As(target, &t) {
		return e.code == t.code
	}
	return false
}

// WithCode wraps err with the given gRPC code. If err already carries a
// code via codedError, the new code replaces the old one (the inner
// err — including any sentinel — is preserved). Wrapping nil returns
// nil so call sites can write `return WithCode(codes.X, op())`
// unconditionally.
func WithCode(code codes.Code, err error) error {
	if err == nil {
		return nil
	}
	var existing *codedError
	if errors.As(err, &existing) {
		return &codedError{code: code, err: existing.err}
	}
	return &codedError{code: code, err: err}
}
