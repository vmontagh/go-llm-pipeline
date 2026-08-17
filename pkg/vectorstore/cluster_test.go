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
			// a, b, c lie on an arc at 0°, 35°, 70°. At threshold 0.6 the
			// adjacent pairs a-b and b-c meet it (cos35° ≈ 0.82) but the ends
			// a-c do not (cos70° ≈ 0.34), so all three group only via
			// transitive single linkage. x, y form a separate pair far enough
			// away not to connect to the arc.
			items: []vectorstore.Item{
				{ID: "c", Vector: vectorstore.Vector{0.342020, 0.939693}},  // 70°
				{ID: "a", Vector: vectorstore.Vector{1, 0}},                // 0°
				{ID: "b", Vector: vectorstore.Vector{0.819152, 0.573576}},  // 35°
				{ID: "x", Vector: vectorstore.Vector{-0.906308, 0.422618}}, // 155°
				{ID: "y", Vector: vectorstore.Vector{-0.939693, 0.342020}}, // 160°
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
			name: "identical vectors cluster at endpoint threshold 1",
			// Identical {1,1} vectors normalize to the same unit vector, so
			// cosine reports exactly 1 and they link at threshold 1.
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 1}},
				{ID: "b", Vector: vectorstore.Vector{1, 1}},
			},
			threshold: 1,
			want:      [][]string{{"a", "b"}},
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
