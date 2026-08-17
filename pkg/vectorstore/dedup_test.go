package vectorstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// pairIDs projects duplicates to their (A.ID, B.ID) pairs for order-sensitive
// comparison. It returns nil for an empty result so it compares cleanly against
// a nil want.
func pairIDs(dups []vectorstore.Duplicate) [][2]string {
	if len(dups) == 0 {
		return nil
	}
	out := make([][2]string, len(dups))
	for i, d := range dups {
		out[i] = [2]string{d.A.ID, d.B.ID}
	}
	return out
}

func TestFindDuplicates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []vectorstore.Item
		threshold float64
		want      [][2]string
	}{
		{
			name: "identical and near vectors pair up",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{1, 0}},    // identical to a
				{ID: "c", Vector: vectorstore.Vector{0.99, 0}}, // same direction as a and b
				{ID: "d", Vector: vectorstore.Vector{0, 1}},    // orthogonal
			},
			threshold: 0.999,
			want:      [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}},
		},
		{
			name: "unsorted input yields sorted pairs",
			items: []vectorstore.Item{
				{ID: "z", Vector: vectorstore.Vector{1, 0}},
				{ID: "m", Vector: vectorstore.Vector{1, 0}},
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
			},
			threshold: 0.5,
			want:      [][2]string{{"a", "m"}, {"a", "z"}, {"m", "z"}},
		},
		{
			name: "identical vectors meet endpoint threshold 1",
			// Independent normalization would score identical {1,1} vectors a
			// hair below 1; the cosine endpoint snap keeps them paired at 1.
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 1}},
				{ID: "b", Vector: vectorstore.Vector{1, 1}},
			},
			threshold: 1,
			want:      [][2]string{{"a", "b"}},
		},
		{
			name: "opposite vectors meet endpoint threshold -1",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{-1, 0}},
			},
			threshold: -1,
			want:      [][2]string{{"a", "b"}},
		},
		{
			name: "no pairs meet threshold",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}},
			},
			threshold: 0.9,
			want:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := vectorstore.FindDuplicates(t.Context(), tc.items, tc.threshold)
			if err != nil {
				t.Fatalf("FindDuplicates(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(tc.want, pairIDs(got)); diff != "" {
				t.Errorf("FindDuplicates(%s) pairs mismatch (-want +got):\n%s", tc.name, diff)
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
			items:     []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			threshold: -1.5,
			want:      vectorstore.ErrInvalidThreshold,
		},
		{
			name:      "threshold above range",
			items:     []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			threshold: 1.5,
			want:      vectorstore.ErrInvalidThreshold,
		},
		{
			name: "dimension mismatch",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
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
			items:     []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{0, 0}}},
			threshold: 0.9,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicate id",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "a", Vector: vectorstore.Vector{0, 1}},
			},
			threshold: 0.9,
			want:      vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.FindDuplicates(t.Context(), tc.items, tc.threshold)
			if !errors.Is(err, tc.want) {
				t.Errorf("FindDuplicates(%s) = %v, want %v", tc.name, err, tc.want)
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
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{1, 0}},
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
