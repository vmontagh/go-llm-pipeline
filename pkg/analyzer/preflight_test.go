package analyzer

import (
	"context"
	"errors"
	"testing"
)

var errTestPreflightFailed = errors.New("preflight failed")

type mockPreflighter struct {
	err error
}

func (m mockPreflighter) Preflight(ctx context.Context) error {
	return m.err
}

func TestRunPreflight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pf      Preflighter
		wantErr error
	}{
		{
			name: "nil preflighter is no-op",
		},
		{
			name: "preflight success",
			pf:   mockPreflighter{},
		},
		{
			name:    "preflight error",
			pf:      mockPreflighter{err: errTestPreflightFailed},
			wantErr: errTestPreflightFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := RunPreflight(t.Context(), tt.pf)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("RunPreflight(ctx, %v) = %v, want %v", tt.pf, err, tt.wantErr)
			}
		})
	}
}
