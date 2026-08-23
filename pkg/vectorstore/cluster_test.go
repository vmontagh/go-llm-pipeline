package vectorstore_test

import (
	"errors"
	"math"
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

// axisDistance is a non-cosine, 1-D metric: the absolute gap between the items'
// first vector component treated as a position on a line. ClusterByDistance
// never normalizes or dimension-checks vectors, so this exercises the shared
// core with a distance that has nothing to do with cosine similarity, proving
// the clusterer is metric-independent.
func axisDistance(a, b vectorstore.Item) float64 {
	return math.Abs(a.Vector[0] - b.Vector[0])
}

// axis builds an item whose only meaningful data is its 1-D position, the shape
// a scalar metric (time, price, ...) consumes.
func axis(id string, pos float64) vectorstore.Item {
	return vectorstore.Item{ID: id, Vector: vectorstore.Vector{pos}}
}

func TestClusterByDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		items       []vectorstore.Item
		maxDistance float64
		want        [][]string
	}{
		{
			name: "no edges yields singletons",
			items: []vectorstore.Item{
				axis("b", 10),
				axis("a", 0),
				axis("c", 20),
			},
			maxDistance: 5,
			want:        [][]string{{"a"}, {"b"}, {"c"}},
		},
		{
			name: "fully connected group is one cluster",
			items: []vectorstore.Item{
				axis("c", 2),
				axis("a", 0),
				axis("b", 1),
			},
			maxDistance: 5,
			want:        [][]string{{"a", "b", "c"}},
		},
		{
			name: "transitive chain links distant endpoints",
			// 0-1-2-3 are each within maxDistance 1 of the next neighbor, so
			// single linkage joins all four even though a and d are 3 apart.
			items: []vectorstore.Item{
				axis("d", 3),
				axis("a", 0),
				axis("c", 2),
				axis("b", 1),
			},
			maxDistance: 1,
			want:        [][]string{{"a", "b", "c", "d"}},
		},
		{
			name: "gap splits into separate clusters",
			items: []vectorstore.Item{
				axis("a", 0),
				axis("b", 1),
				axis("x", 10),
				axis("y", 11),
			},
			maxDistance: 1,
			want:        [][]string{{"a", "b"}, {"x", "y"}},
		},
		{
			name:        "no items",
			items:       nil,
			maxDistance: 1,
			want:        nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := vectorstore.ClusterByDistance(tc.items, axisDistance, tc.maxDistance)
			if err != nil {
				t.Fatalf("ClusterByDistance(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(tc.want, clusterIDs(got)); diff != "" {
				t.Errorf("ClusterByDistance(%s) mismatch (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

// TestClusterByDistanceDeterministic pins down that clustering depends only on
// the set of items, not their input order: every permutation of a fixed set
// must produce the same sorted clusters.
func TestClusterByDistanceDeterministic(t *testing.T) {
	t.Parallel()

	want := [][]string{{"a", "b", "c"}, {"x", "y"}}
	orderings := [][]vectorstore.Item{
		{axis("a", 0), axis("b", 1), axis("c", 2), axis("x", 10), axis("y", 11)},
		{axis("y", 11), axis("x", 10), axis("c", 2), axis("b", 1), axis("a", 0)},
		{axis("c", 2), axis("x", 10), axis("a", 0), axis("y", 11), axis("b", 1)},
	}

	for _, items := range orderings {
		got, err := vectorstore.ClusterByDistance(items, axisDistance, 1)
		if err != nil {
			t.Fatalf("ClusterByDistance(%v) = %v, want nil", clusterInputIDs(items), err)
		}
		if diff := cmp.Diff(want, clusterIDs(got)); diff != "" {
			t.Errorf("ClusterByDistance(%v) mismatch (-want +got):\n%s", clusterInputIDs(items), diff)
		}
	}
}

func TestClusterByDistanceErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []vectorstore.Item
		want  error
	}{
		{
			name:  "empty id",
			items: []vectorstore.Item{axis("", 0)},
			want:  vectorstore.ErrEmptyID,
		},
		{
			name:  "duplicate id",
			items: []vectorstore.Item{axis("a", 0), axis("a", 1)},
			want:  vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.ClusterByDistance(tc.items, axisDistance, 1)
			if !errors.Is(err, tc.want) {
				t.Errorf("ClusterByDistance(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// clusterInputIDs lists item IDs in input order for readable failure messages.
func clusterInputIDs(items []vectorstore.Item) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}
