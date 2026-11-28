package analyzer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var errClassifyTest = errors.New("rate limited")

func TestHandleProviderError_ReportsEffectiveCooldown(t *testing.T) {
	t.Parallel()
	const baseCooldown = 5 * time.Second
	tests := []struct {
		name            string
		honorRetryAfter bool
		retryAfter      time.Duration
		wantCooldown    time.Duration
	}{
		{
			name:            "retry-after floor applied when honored and larger",
			honorRetryAfter: true,
			retryAfter:      30 * time.Second,
			wantCooldown:    30 * time.Second,
		},
		{
			name:            "cooldown used when retry-after smaller",
			honorRetryAfter: true,
			retryAfter:      time.Second,
			wantCooldown:    baseCooldown,
		},
		{
			name:            "retry-after ignored when not honored",
			honorRetryAfter: false,
			retryAfter:      30 * time.Second,
			wantCooldown:    baseCooldown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := provider.NewKeyPoolWithClock([]string{"k"}, newFakeClock())
			lease, err := pool.Acquire(context.Background())
			if err != nil {
				t.Fatalf("Acquire() error: %v", err)
			}

			var gotMsg string
			rp := policy.RetryPolicy{Tiers: map[policy.ErrorTier]policy.TierPolicy{
				policy.TierShortRateLimit: {Cooldown: baseCooldown, HonorRetryAfter: tt.honorRetryAfter},
			}}
			req := handleProviderErrorRequest[string]{
				Lease:      lease,
				Tier:       policy.TierShortRateLimit,
				Err:        errClassifyTest,
				Policy:     rp,
				RetryAfter: tt.retryAfter,
				Info: providerErrorInfo{
					Provider:        "mock",
					CredentialCount: 1,
					Publish: func(_, _, message string, _ bool) {
						gotMsg = message
					},
					Logger: discardLogger,
					Tracer: noOpTracer{},
				},
			}

			// handleProviderError always returns a wrapped error for the
			// rate-limit tier; assert it preserves the underlying cause.
			gotErr := handleProviderError(context.Background(), req)
			if !errors.Is(gotErr, errClassifyTest) {
				t.Fatalf("handleProviderError() error = %v, want wrapping %v", gotErr, errClassifyTest)
			}

			want := fmt.Sprintf("cooled for %v", tt.wantCooldown)
			if !strings.Contains(gotMsg, want) {
				t.Errorf("Publish message = %q, want substring %q", gotMsg, want)
			}
		})
	}
}
