package analyzer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

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
