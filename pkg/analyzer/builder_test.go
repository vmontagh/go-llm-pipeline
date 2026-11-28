package analyzer

import (
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

func TestBuilderWithGuard(t *testing.T) {
	type outcome struct {
		Guarded bool
		Err     error
	}
	tests := []struct {
		name      string
		nilProv   bool
		guard     bool
		guardOpts []provider.GuardOption
		want      outcome
	}{
		{name: "noGuard"},
		{name: "guarded", guard: true, want: outcome{Guarded: true}},
		{name: "invalidGuard", guard: true, guardOpts: []provider.GuardOption{provider.WithConcurrencyLimits(0, 1)}, want: outcome{Err: provider.ErrInvalidGuardConfig}},
		{name: "nilProviderGuarded", nilProv: true, guard: true, want: outcome{Err: ErrNilProvider}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p provider.Provider[string] = newMockProvider(&provider.CallResult{})
			if tt.nilProv {
				p = nil
			}
			b := NewBuilder(p, func(s string) (string, error) { return s, nil })
			if tt.guard {
				b.WithGuard(tt.guardOpts...)
			}

			tmpl, err := b.Build()

			got := outcome{Err: err}
			if err == nil {
				_, got.Guarded = tmpl.Provider().(*provider.Guard[string])
			}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Build() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuilderOptions(t *testing.T) {
	type config struct {
		Scrubbed      string
		MaxAssetBytes int64
		ShortCooldown time.Duration
		BackOff       backoff.BackOff
		RetryPolicy   policy.RetryPolicy
	}
	const (
		customMaxAssetBytes = 10
		customCooldown      = time.Second
	)
	customPolicy := policy.RetryPolicy{Tiers: map[policy.ErrorTier]policy.TierPolicy{policy.TierShortTransient: {}}}
	defaults := config{
		Scrubbed:      "s",
		MaxAssetBytes: DefaultMaxAssetBytes,
		ShortCooldown: defaultShortCooldownRetryThreshold,
		RetryPolicy:   policy.DefaultRetryPolicy(),
	}
	tests := []struct {
		name      string
		configure func(*Builder[string, string])
		want      config
	}{
		{name: "defaults", configure: func(*Builder[string, string]) {}, want: defaults},
		{
			name: "set",
			configure: func(b *Builder[string, string]) {
				b.WithScrubFunc(strings.ToUpper).WithMaxAssetBytes(customMaxAssetBytes).WithShortCooldownRetryThreshold(customCooldown).
					WithBackOff(&backoff.StopBackOff{}).WithRetryPolicy(customPolicy)
			},
			want: config{Scrubbed: "S", MaxAssetBytes: customMaxAssetBytes, ShortCooldown: customCooldown, BackOff: &backoff.StopBackOff{}, RetryPolicy: customPolicy},
		},
		{
			name: "invalid values ignored",
			configure: func(b *Builder[string, string]) {
				b.WithScrubFunc(nil).WithMaxAssetBytes(-1).WithBackOff(nil).WithRetryPolicy(policy.RetryPolicy{})
			},
			want: defaults,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder(newMockProvider(&provider.CallResult{}), func(s string) (string, error) { return s, nil })
			tt.configure(b)

			tmpl, err := b.Build()
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}

			got := config{
				Scrubbed:      tmpl.scrubFn("s"),
				MaxAssetBytes: tmpl.maxAssetBytes,
				ShortCooldown: tmpl.shortCooldown,
				BackOff:       tmpl.backOff,
				RetryPolicy:   tmpl.retryPolicy,
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Build() config mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
