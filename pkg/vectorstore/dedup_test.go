package vectorstore_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// scoreTolerance absorbs float rounding in cosine scores; the vectorstore
// docs only promise exact endpoints for identical and antipodal directions.
const scoreTolerance = 1e-9

func TestFindDuplicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []vectorstore.Item
		threshold float64
		want      []pair
	}{
		{
			name: "identical and near vectors pair up",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 1, 0),    // identical to a
				item("c", 0.99, 0), // same direction as a and b
				item("d", 0, 1),    // orthogonal
			},
			threshold: 0.999,
			want:      []pair{{"a", "b", 1}, {"a", "c", 1}, {"b", "c", 1}},
		},
		{
			name: "unsorted input yields sorted pairs",
			items: []vectorstore.Item{
				item("z", 1, 0),
				item("m", 1, 0),
				item("a", 1, 0),
			},
			threshold: 0.5,
			want:      []pair{{"a", "m", 1}, {"a", "z", 1}, {"m", "z", 1}},
		},
		{
			name: "identical vectors meet endpoint threshold 1",
			// Identical {1,1} vectors normalize to the same unit vector, so
			// cosine reports exactly 1 and they pair at threshold 1.
			items: []vectorstore.Item{
				item("a", 1, 1),
				item("b", 1, 1),
			},
			threshold: 1,
			want:      []pair{{"a", "b", 1}},
		},
		{
			name: "opposite vectors meet endpoint threshold -1",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", -1, 0),
			},
			threshold: -1,
			want:      []pair{{"a", "b", -1}},
		},
		{
			name: "near-identical distinct directions excluded at threshold 1",
			// {1,0} and {1,1e-5} have cosine just below 1; they must not be
			// snapped together at the exact endpoint.
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 1, 1e-5),
			},
			threshold: 1,
			want:      nil,
		},
		{
			name: "no pairs meet threshold",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 0, 1),
			},
			threshold: 0.9,
			want:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := wantDuplicates(t, snapshotByID(t, tc.items), tc.want)

			got, err := vectorstore.FindDuplicates(t.Context(), tc.items, tc.threshold)
			if err != nil {
				t.Fatalf("FindDuplicates(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty(), cmpopts.EquateApprox(0, scoreTolerance)); diff != "" {
				t.Errorf("FindDuplicates(%s) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestFindDuplicatesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []vectorstore.Item
		threshold float64
		want      error
	}{
		{
			name:      "threshold below range",
			items:     []vectorstore.Item{item("a", 1, 0)},
			threshold: -1.5,
			want:      vectorstore.ErrInvalidThreshold,
		},
		{
			name:      "threshold above range",
			items:     []vectorstore.Item{item("a", 1, 0)},
			threshold: 1.5,
			want:      vectorstore.ErrInvalidThreshold,
		},
		{
			name: "dimension mismatch",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 1, 0, 0),
			},
			threshold: 0.9,
			want:      vectorstore.ErrDimensionMismatch,
		},
		{
			name:      "empty id",
			items:     []vectorstore.Item{{Vector: vectorstore.Vector{1, 0}}},
			threshold: 0.9,
			want:      vectorstore.ErrEmptyID,
		},
		{
			name:      "empty vector",
			items:     []vectorstore.Item{{ID: "a"}},
			threshold: 0.9,
			want:      vectorstore.ErrEmptyVector,
		},
		{
			name:      "zero magnitude vector",
			items:     []vectorstore.Item{item("a", 0, 0)},
			threshold: 0.9,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			// Invalid components sit past index 0 so per-component validation
			// of only the first element would not pass.
			name:      "NaN component",
			items:     []vectorstore.Item{item("a", 1, math.NaN())},
			threshold: 0.9,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name:      "positive infinity component",
			items:     []vectorstore.Item{item("a", 1, math.Inf(1))},
			threshold: 0.9,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name:      "negative infinity component",
			items:     []vectorstore.Item{item("a", math.Inf(-1), 0)},
			threshold: 0.9,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicate id",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("a", 0, 1),
			},
			threshold: 0.9,
			want:      vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.FindDuplicates(t.Context(), tc.items, tc.threshold)
			if diff := cmp.Diff(tc.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("FindDuplicates(%s) error mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestFindDuplicatesCancelled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []vectorstore.Item
	}{
		{
			name:  "empty items",
			items: nil,
		},
		{
			name: "non-empty items",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 1, 0),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := vectorstore.FindDuplicates(ctx, tc.items, 0.9); !errors.Is(err, context.Canceled) {
				t.Errorf("FindDuplicates(%s, cancelled ctx) = %v, want context.Canceled", tc.name, err)
			}
		})
	}
}
