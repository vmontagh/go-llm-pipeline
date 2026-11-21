package vectorstore_test

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

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
				item("c", 0.342020, 0.939693),  // 70°
				item("a", 1, 0),                // 0°
				item("b", 0.819152, 0.573576),  // 35°
				item("x", -0.906308, 0.422618), // 155°
				item("y", -0.939693, 0.342020), // 160°
			},
			threshold: 0.6,
			want:      [][]string{{"a", "b", "c"}, {"x", "y"}},
		},
		{
			name: "high threshold yields singletons",
			items: []vectorstore.Item{
				item("b", 0, 1),
				item("a", 1, 0),
			},
			threshold: 0.99,
			want:      [][]string{{"a"}, {"b"}},
		},
		{
			name: "identical vectors cluster at endpoint threshold 1",
			// Identical {1,1} vectors normalize to the same unit vector, so
			// cosine reports exactly 1 and they link at threshold 1.
			items: []vectorstore.Item{
				item("a", 1, 1),
				item("b", 1, 1),
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
		{
			name: "orthogonal vectors stay split at subnormal threshold",
			// Regression: cosine of orthogonal unit vectors is exactly 0, which
			// is below the smallest positive threshold, so they must remain
			// separate. A subtractive `1-cosine <= 1-threshold` form would round
			// 1-threshold up to 1.0 and wrongly merge them.
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("b", 0, 1),
			},
			threshold: math.SmallestNonzeroFloat64,
			want:      [][]string{{"a"}, {"b"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want := wantClusters(t, snapshotByID(t, tc.items), tc.want)

			got, err := vectorstore.ClusterItems(tc.items, tc.threshold)
			if err != nil {
				t.Fatalf("ClusterItems(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
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
			items:     []vectorstore.Item{item("a", 0, 0)},
			threshold: 0.5,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			// Invalid components sit past index 0 so per-component validation
			// of only the first element would not pass.
			name:      "NaN component",
			items:     []vectorstore.Item{item("a", 1, math.NaN())},
			threshold: 0.5,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name:      "positive infinity component",
			items:     []vectorstore.Item{item("a", 1, math.Inf(1))},
			threshold: 0.5,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name:      "negative infinity component",
			items:     []vectorstore.Item{item("a", math.Inf(-1), 0)},
			threshold: 0.5,
			want:      vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicate id",
			items: []vectorstore.Item{
				item("a", 1, 0),
				item("a", 0, 1),
			},
			threshold: 0.5,
			want:      vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.ClusterItems(tc.items, tc.threshold)
			if diff := cmp.Diff(tc.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ClusterItems(%s) error mismatch (-want +got):\n%s", tc.name, diff)
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
	return item(id, pos)
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
			want := wantClusters(t, snapshotByID(t, tc.items), tc.want)

			got, err := vectorstore.ClusterByDistance(tc.items, axisDistance, tc.maxDistance)
			if err != nil {
				t.Fatalf("ClusterByDistance(%s) = %v, want nil", tc.name, err)
			}
			if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
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

	wantGroups := [][]string{{"a", "b", "c"}, {"x", "y"}}
	orderings := [][]vectorstore.Item{
		{axis("a", 0), axis("b", 1), axis("c", 2), axis("x", 10), axis("y", 11)},
		{axis("y", 11), axis("x", 10), axis("c", 2), axis("b", 1), axis("a", 0)},
		{axis("c", 2), axis("x", 10), axis("a", 0), axis("y", 11), axis("b", 1)},
	}

	for _, items := range orderings {
		want := wantClusters(t, snapshotByID(t, items), wantGroups)

		got, err := vectorstore.ClusterByDistance(items, axisDistance, 1)
		if err != nil {
			t.Fatalf("ClusterByDistance(%v) = %v, want nil", clusterInputIDs(items), err)
		}
		if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("ClusterByDistance(%v) mismatch (-want +got):\n%s", clusterInputIDs(items), diff)
		}
	}
}

func TestClusterByDistanceErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		items  []vectorstore.Item
		metric vectorstore.Metric
		want   error
	}{
		{
			name:   "empty id",
			items:  []vectorstore.Item{axis("", 0)},
			metric: axisDistance,
			want:   vectorstore.ErrEmptyID,
		},
		{
			name:   "duplicate id",
			items:  []vectorstore.Item{axis("a", 0), axis("a", 1)},
			metric: axisDistance,
			want:   vectorstore.ErrDuplicateID,
		},
		{
			name:   "nil metric with multiple items",
			items:  []vectorstore.Item{axis("a", 0), axis("b", 1)},
			metric: nil,
			want:   vectorstore.ErrNilMetric,
		},
		{
			name:   "nil metric with single item",
			items:  []vectorstore.Item{axis("a", 0)},
			metric: nil,
			want:   vectorstore.ErrNilMetric,
		},
		{
			name:   "nil metric with no items",
			items:  nil,
			metric: nil,
			want:   vectorstore.ErrNilMetric,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.ClusterByDistance(tc.items, tc.metric, 1)
			if diff := cmp.Diff(tc.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ClusterByDistance(%s) error mismatch (-want +got):\n%s", tc.name, diff)
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
