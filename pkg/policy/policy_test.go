package policy_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc/codes"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var (
	errPlain = errors.New("plain failure")
	errOther = errors.New("other failure")
)

func TestRetryPolicyLookup(t *testing.T) {
	t.Parallel()

	const cooldown = 7 * time.Second
	configured := policy.TierPolicy{Cooldown: cooldown}

	tests := []struct {
		name   string
		policy policy.RetryPolicy
		tier   policy.ErrorTier
		want   policy.TierPolicy
	}{
		{
			name: "nil tiers map falls back to zero policy",
			tier: policy.TierShortTransient,
		},
		{
			name:   "configured tier",
			policy: policy.RetryPolicy{Tiers: map[policy.ErrorTier]policy.TierPolicy{policy.TierShortTransient: configured}},
			tier:   policy.TierShortTransient,
			want:   configured,
		},
		{
			name:   "unconfigured tier falls back to zero policy",
			policy: policy.RetryPolicy{Tiers: map[policy.ErrorTier]policy.TierPolicy{policy.TierShortTransient: configured}},
			tier:   policy.TierTerminal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.policy.Lookup(tc.tier)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Lookup(%v) mismatch (-want +got):\n%s", tc.tier, diff)
			}
		})
	}
}

// TestDefaultRetryPolicyBoundsRetries pins the safety properties of the
// default policy rather than its exact durations: every tier is configured,
// the bounded tier caps L2 retries, and backoffs never exceed their maximum.
func TestDefaultRetryPolicyBoundsRetries(t *testing.T) {
	t.Parallel()

	p := policy.DefaultRetryPolicy()
	tiers := []policy.ErrorTier{
		policy.TierTerminal,
		policy.TierShortTransient,
		policy.TierShortRateLimit,
		policy.TierLongTransientBounded,
		policy.TierLongTransientInfinite,
	}
	for _, tier := range tiers {
		tp, ok := p.Tiers[tier]
		if !ok {
			t.Errorf("DefaultRetryPolicy().Tiers[%v] missing", tier)
			continue
		}
		if tp.L2BaseBackoff > tp.L2MaxBackoff {
			t.Errorf("DefaultRetryPolicy() tier %v: L2BaseBackoff %v > L2MaxBackoff %v", tier, tp.L2BaseBackoff, tp.L2MaxBackoff)
		}
	}
	if got := p.Lookup(policy.TierLongTransientBounded).L2MaxRetries; got <= 0 {
		t.Errorf("DefaultRetryPolicy().Lookup(TierLongTransientBounded).L2MaxRetries = %d, want > 0", got)
	}
}

func TestDefaultCodeToTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		code codes.Code
		want policy.ErrorTier
	}{
		{code: codes.ResourceExhausted, want: policy.TierLongTransientInfinite},
		{code: codes.FailedPrecondition, want: policy.TierLongTransientBounded},
		{code: codes.Unavailable, want: policy.TierShortTransient},
		{code: codes.InvalidArgument, want: policy.TierTerminal},
		{code: codes.OK, want: policy.TierTerminal},
	}

	for _, tc := range tests {
		if got := policy.DefaultCodeToTier(tc.code); got != tc.want {
			t.Errorf("DefaultCodeToTier(%v) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestTierCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier policy.ErrorTier
		err  error
		want codes.Code
	}{
		{name: "short transient", tier: policy.TierShortTransient, err: errPlain, want: codes.Unavailable},
		{name: "short rate limit", tier: policy.TierShortRateLimit, err: errPlain, want: codes.ResourceExhausted},
		{name: "long bounded", tier: policy.TierLongTransientBounded, err: errPlain, want: codes.FailedPrecondition},
		{name: "long infinite", tier: policy.TierLongTransientInfinite, err: errPlain, want: codes.ResourceExhausted},
		{name: "terminal keeps embedded code", tier: policy.TierTerminal, err: policy.WithCode(codes.Unauthenticated, errPlain), want: codes.Unauthenticated},
		{name: "terminal unknown error", tier: policy.TierTerminal, err: errPlain, want: codes.Internal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.TierCode(tc.tier, tc.err); got != tc.want {
				t.Errorf("TierCode(%v, %v) = %v, want %v", tc.tier, tc.err, got, tc.want)
			}
		})
	}
}

// TestDefaultCodeToTierRoundTrip checks that every retryable tier survives the
// Tier -> Code -> Tier round trip the orchestrator relies on.
func TestDefaultCodeToTierRoundTrip(t *testing.T) {
	t.Parallel()

	tiers := []policy.ErrorTier{
		policy.TierShortTransient,
		policy.TierLongTransientBounded,
		policy.TierLongTransientInfinite,
	}
	for _, tier := range tiers {
		if got := policy.DefaultCodeToTier(policy.TierCode(tier, nil)); got != tier {
			t.Errorf("DefaultCodeToTier(TierCode(%v, nil)) = %v, want %v", tier, got, tier)
		}
	}
}

func TestClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		err               error
		wantCode          codes.Code
		wantRetryable     bool
		wantLongRetryable bool
	}{
		{name: "nil", wantCode: codes.OK},
		{name: "canceled", err: fmt.Errorf("call: %w", context.Canceled), wantCode: codes.Canceled},
		{name: "deadline", err: fmt.Errorf("call: %w", context.DeadlineExceeded), wantCode: codes.DeadlineExceeded},
		{name: "unknown", err: errPlain, wantCode: codes.Internal},
		{
			name:          "coded unavailable",
			err:           fmt.Errorf("outer: %w", policy.WithCode(codes.Unavailable, errPlain)),
			wantCode:      codes.Unavailable,
			wantRetryable: true,
		},
		{
			name:              "coded code wins over sentinel",
			err:               policy.WithCode(codes.ResourceExhausted, provider.ErrMissingAPIKey),
			wantCode:          codes.ResourceExhausted,
			wantRetryable:     true,
			wantLongRetryable: true,
		},
		{
			name:              "quota sentinel",
			err:               fmt.Errorf("gemini: %w", provider.ErrQuotaExceeded),
			wantCode:          codes.FailedPrecondition,
			wantRetryable:     true,
			wantLongRetryable: true,
		},
		{
			name:              "rate limit sentinel",
			err:               provider.ErrRateLimitExceeded,
			wantCode:          codes.ResourceExhausted,
			wantRetryable:     true,
			wantLongRetryable: true,
		},
		{
			name:              "all keys cooling down sentinel",
			err:               provider.ErrAllKeysCoolingDown,
			wantCode:          codes.ResourceExhausted,
			wantRetryable:     true,
			wantLongRetryable: true,
		},
		{name: "missing key sentinel", err: provider.ErrMissingAPIKey, wantCode: codes.Unauthenticated},
		{name: "empty candidate sentinel", err: provider.ErrEmptyCandidate, wantCode: codes.InvalidArgument},
		{name: "invalid part sentinel", err: provider.ErrInvalidResponsePart, wantCode: codes.InvalidArgument},
		{
			name:          "unexpected status sentinel",
			err:           provider.ErrUnexpectedStatus,
			wantCode:      codes.Unavailable,
			wantRetryable: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := policy.Code(tc.err); got != tc.wantCode {
				t.Errorf("Code(%v) = %v, want %v", tc.err, got, tc.wantCode)
			}
			if got := policy.IsRetryable(tc.err); got != tc.wantRetryable {
				t.Errorf("IsRetryable(%v) = %t, want %t", tc.err, got, tc.wantRetryable)
			}
			if got := policy.IsLongRetryable(tc.err); got != tc.wantLongRetryable {
				t.Errorf("IsLongRetryable(%v) = %t, want %t", tc.err, got, tc.wantLongRetryable)
			}
		})
	}
}

func TestWithCodeNil(t *testing.T) {
	t.Parallel()

	if got := policy.WithCode(codes.Unavailable, nil); got != nil {
		t.Errorf("WithCode(Unavailable, nil) = %v, want nil", got)
	}
}

func TestWithCodeRecodePreservesInnerError(t *testing.T) {
	t.Parallel()

	first := policy.WithCode(codes.Unavailable, provider.ErrQuotaExceeded)
	got := policy.WithCode(codes.FailedPrecondition, first)

	if c := policy.Code(got); c != codes.FailedPrecondition {
		t.Errorf("Code(WithCode(FailedPrecondition, %v)) = %v, want %v", first, c, codes.FailedPrecondition)
	}
	if !errors.Is(got, provider.ErrQuotaExceeded) {
		t.Errorf("errors.Is(%v, ErrQuotaExceeded) = false, want true", got)
	}
	if errors.Is(got, first) {
		t.Errorf("errors.Is(%v, %v) = true, want false: the replaced code must not match", got, first)
	}
}

func TestCodedErrorIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{
			name:   "same code different inner error",
			err:    policy.WithCode(codes.Unavailable, errPlain),
			target: policy.WithCode(codes.Unavailable, errOther),
			want:   true,
		},
		{
			name:   "different code",
			err:    policy.WithCode(codes.Unavailable, errPlain),
			target: policy.WithCode(codes.Internal, errPlain),
		},
		{
			name:   "uncoded target matches inner error",
			err:    policy.WithCode(codes.Unavailable, errPlain),
			target: errPlain,
			want:   true,
		},
		{
			name:   "uncoded unrelated target",
			err:    policy.WithCode(codes.Unavailable, errPlain),
			target: errOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(tc.err, tc.target); got != tc.want {
				t.Errorf("errors.Is(%v, %v) = %t, want %t", tc.err, tc.target, got, tc.want)
			}
		})
	}
}

func TestCodedErrorMessageKeepsCause(t *testing.T) {
	t.Parallel()

	err := policy.WithCode(codes.Unavailable, errPlain)
	want := codes.Unavailable.String() + ": " + errPlain.Error()
	if got := err.Error(); got != want {
		t.Errorf("WithCode(Unavailable, %v).Error() = %q, want %q", errPlain, got, want)
	}
}
