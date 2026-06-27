package analyzer

import (
	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"google.golang.org/grpc/codes"
)

// RetryPolicy / TierPolicy / ErrorTier are type aliases
type (
	RetryPolicy = policy.RetryPolicy
	TierPolicy  = policy.TierPolicy
	ErrorTier   = policy.ErrorTier
)

// Tier constants re-exported from policy.
const (
	TierTerminal              = policy.TierTerminal
	TierShortTransient        = policy.TierShortTransient
	TierShortRateLimit        = policy.TierShortRateLimit
	TierLongTransientBounded  = policy.TierLongTransientBounded
	TierLongTransientInfinite = policy.TierLongTransientInfinite
)

// DefaultRetryPolicy returns policy.DefaultRetryPolicy().
func DefaultRetryPolicy() RetryPolicy { return policy.DefaultRetryPolicy() }

// DefaultCodeToTier returns policy.DefaultCodeToTier(code).
func DefaultCodeToTier(code codes.Code) ErrorTier { return policy.DefaultCodeToTier(code) }

// TierCode returns policy.TierCode(tier, err).
func TierCode(tier ErrorTier, err error) codes.Code {
	if tier == TierTerminal {
		return Code(err)
	}
	return policy.TierCode(tier, err)
}

// WithCode returns policy.WithCode(code, err).
func WithCode(code codes.Code, err error) error { return policy.WithCode(code, err) }
