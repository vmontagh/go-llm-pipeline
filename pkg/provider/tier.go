package provider

import "time"

// ErrorTier is the actionable retry category returned by a Provider's
// ClassifyError method. The retry engine routes the payload purely on
// the returned tier — it does not inspect the error itself.
type ErrorTier int

const (
	// TierTerminal: the error cannot be recovered from; send to DLQ.
	TierTerminal ErrorTier = iota
	// TierShortTransient: retry in-call (L1), possibly rotating keys.
	// Typical: transient 5xx, network blips.
	TierShortTransient
	// TierShortRateLimit: retry in-call (L1) with key rotation.
	// Typical: per-key temporary rate limits where another key may succeed.
	TierShortRateLimit
	// TierLongTransientBounded: exit L1 immediately and re-queue at L2
	// with a bounded retry budget. Typical: hard quota exhaustion.
	TierLongTransientBounded
	// TierLongTransientInfinite: exit L1 immediately and re-queue at L2
	// with an infinite retry budget (temporary throttle always clears).
	TierLongTransientInfinite
)

// ClassifyInput holds the pre-extracted HTTP metadata passed to
// Provider.ClassifyError. StatusCode is taken from *http.Response.StatusCode
// (0 when no HTTP response is available). Body holds the already-read
// response bytes so callers never re-read a closed resp.Body. RetryAfter
// is the server-suggested retry delay parsed from the Retry-After header
// (0 when no header was present).
type ClassifyInput struct {
	Err        error
	StatusCode int
	Body       []byte
	RetryAfter time.Duration
}

// RetryContext carries dynamic state about the current retry attempt,
// allowing ClassifyError implementations to make context-aware routing
// decisions (for example, escalating tiers after a threshold).
type RetryContext struct {
	// Attempt is the zero-based attempt index (0 = first try).
	Attempt int
	// Elapsed is the time elapsed since the first Analyze call.
	Elapsed time.Duration
}
