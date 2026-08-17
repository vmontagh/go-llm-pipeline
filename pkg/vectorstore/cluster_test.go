package vectorstore_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// clusterIDs projects clusters to their member IDs for comparison. It returns
// nil for an empty result so it compares cleanly against a nil want.
func clusterIDs(clusters []vectorstore.Cluster) [][]string {
	if len(clusters) == 0 {
		return nil
	}
	out := make([][]string, len(clusters))
	for i, c := range clusters {
		ids := make([]string, len(c.Items))
		for j, item := range c.Items {
			ids[j] = item.ID
		}
		out[i] = ids
	}
	return out
}

func TestClusterItems(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []vectorstore.Item
		threshold float64
		want      [][]string
	}{
		{
			name: "single linkage connects transitive group",
			// a-b-c form a chain of adjacent-close vectors; x-y form a second
			// group. Single-linkage keeps them apart at threshold 0.6.
			items: []vectorstore.Item{
				{ID: "c", Vector: vectorstore.Vector{0.8, 0.2}},
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{0.9, 0.1}},
				{ID: "x", Vector: vectorstore.Vector{0, 1}},
				{ID: "y", Vector: vectorstore.Vector{0.1, 0.9}},
			},
			threshold: 0.6,
			want:      [][]string{{"a", "b", "c"}, {"x", "y"}},
		},
		{
			name: "high threshold yields singletons",
			items: []vectorstore.Item{
				{ID: "b", Vector: vectorstore.Vector{0, 1}},
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
			},
			threshold: 0.99,
			want:      [][]string{{"a"}, {"b"}},
		},
		{
			name:      "no items",
			items:     nil,
			threshold: 0.5,
			want:      nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := vectorstore.ClusterItems(tc.items, tc.threshold)
			if err != nil {
				t.Fatalf("ClusterItems(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(tc.want, clusterIDs(got)); diff != "" {
				t.Errorf("ClusterItems(%s) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

func TestClusterItemsErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		items     []vectorstore.Item
		threshold float64
		want      error
	}{
		{
			name:      "threshold above range",
			items:     []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			threshold: 2,
			want:      vectorstore.ErrInvalidThreshold,
		},
		{
			name: "dimension mismatch",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
			},
			threshold: 0.5,
			want:      vectorstore.ErrDimensionMismatch,
		},
		{
			name:      "empty id",
			items:     []vectorstore.Item{{Vector: vectorstore.Vector{1, 0}}},
			threshold: 0.5,
			want:      vectorstore.ErrEmptyID,
		},
		{
			name:      "empty vector",
			items:     []vectorstore.Item{{ID: "a"}},
			threshold: 0.5,
			want:      vectorstore.ErrEmptyVector,
		},
		{
			name:      "zero magnitude vector",
			items:     []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{0, 0}}},
			threshold: 0.5,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicate id",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "a", Vector: vectorstore.Vector{0, 1}},
			},
			threshold: 0.5,
			want:      vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.ClusterItems(tc.items, tc.threshold)
			if !errors.Is(err, tc.want) {
				t.Errorf("ClusterItems(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
