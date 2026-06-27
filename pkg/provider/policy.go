package provider

import "time"

// TierPolicy is the per-ErrorTier configuration that drives both the
// KeyPool's cooldown decisions and the orchestrator's retry budgets.
//
// Per-tier field applicability:
//
//   - Cooldown: every tier (cools the offending credential).
//   - HonorRetryAfter: rate-limit / quota tiers.
//   - L2MaxRetries: TierLongTransientBounded (bounded L2.1 budget).
//   - L2BaseBackoff / L2MaxBackoff: TierLongTransientBounded and
//     TierLongTransientInfinite (L2 exponential backoff).
//   - EscalateAfterAttempts / EscalateAfter: every escalating tier.
type TierPolicy struct {
	// Cooldown is how long the offending credential is parked in the
	// pool before being eligible for re-acquisition. A zero value
	// means "do not cool down" (the slot is freed immediately).
	Cooldown time.Duration

	// HonorRetryAfter, when true, treats the upstream Retry-After
	// hint as a *floor* for Cooldown.
	HonorRetryAfter bool

	// L2MaxRetries is the orchestrator-level retry budget for the
	// L2.1 bounded path (TierLongTransientBounded).
	L2MaxRetries int

	// L2BaseBackoff and L2MaxBackoff control the L2 exponential backoff.
	L2BaseBackoff time.Duration
	L2MaxBackoff  time.Duration

	// EscalateAfterAttempts and EscalateAfter control the default tier
	// escalation applied before the retry engine acts on a classified
	// error. A zero value disables the corresponding threshold.
	EscalateAfterAttempts int
	EscalateAfter         time.Duration
}
