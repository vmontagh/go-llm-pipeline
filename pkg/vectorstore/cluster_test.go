package vectorstore_test

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

func clusterIDs(clusters []vectorstore.Cluster) [][]string {
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

	// Two tight groups plus transitive linkage. Single-linkage should connect
	// a-b-c (each adjacent pair is close) while keeping x-y separate.
	items := []vectorstore.Item{
		{ID: "c", Vector: vectorstore.Vector{0.8, 0.2}},
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{0.9, 0.1}},
		{ID: "x", Vector: vectorstore.Vector{0, 1}},
		{ID: "y", Vector: vectorstore.Vector{0.1, 0.9}},
	}

	got, err := vectorstore.ClusterItems(items, 0.6)
	if err != nil {
		t.Fatalf("ClusterItems() = %v, want nil", err)
	}

	want := [][]string{{"a", "b", "c"}, {"x", "y"}}
	if diff := cmp.Diff(want, clusterIDs(got)); diff != "" {
		t.Fatalf("clusters mismatch (-want +got):\n%s", diff)
	}
}

func TestClusterItemsSingletons(t *testing.T) {
	t.Parallel()

	// A high threshold leaves every item in its own cluster, sorted by ID.
	items := []vectorstore.Item{
		{ID: "b", Vector: vectorstore.Vector{0, 1}},
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
	}
	got, err := vectorstore.ClusterItems(items, 0.99)
	if err != nil {
		t.Fatalf("ClusterItems() = %v, want nil", err)
	}
	want := [][]string{{"a"}, {"b"}}
	if diff := cmp.Diff(want, clusterIDs(got)); diff != "" {
		t.Fatalf("singleton clusters mismatch (-want +got):\n%s", diff)
	}
}

func TestClusterItemsEmpty(t *testing.T) {
	t.Parallel()

	got, err := vectorstore.ClusterItems(nil, 0.5)
	if err != nil {
		t.Fatalf("ClusterItems(nil) = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("ClusterItems(nil) = %+v, want no clusters", got)
	}
}

func TestClusterItemsInvalidThreshold(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}}
	_, err := vectorstore.ClusterItems(items, 2)
	if !errors.Is(err, vectorstore.ErrInvalidThreshold) {
		t.Fatalf("ClusterItems(threshold=2) = %v, want ErrInvalidThreshold", err)
	}
}

func TestClusterItemsDimensionMismatch(t *testing.T) {
	t.Parallel()

	items := []vectorstore.Item{
		{ID: "a", Vector: vectorstore.Vector{1, 0}},
		{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
	}
	_, err := vectorstore.ClusterItems(items, 0.5)
	if !errors.Is(err, vectorstore.ErrDimensionMismatch) {
		t.Fatalf("ClusterItems(mismatched dims) = %v, want ErrDimensionMismatch", err)
	}
}

func TestClusterItemsInvalidItem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		items []vectorstore.Item
		want  error
	}{
		{
			name:  "emptyIDSingleton",
			items: []vectorstore.Item{{Vector: vectorstore.Vector{1, 0}}},
			want:  vectorstore.ErrEmptyID,
		},
		{
			name:  "emptyVectorSingleton",
			items: []vectorstore.Item{{ID: "a"}},
			want:  vectorstore.ErrEmptyVector,
		},
		{
			name:  "zeroMagnitudeSingleton",
			items: []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{0, 0}}},
			want:  vectorstore.ErrInvalidVector,
		},
		{
			name: "duplicateID",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}},
				{ID: "a", Vector: vectorstore.Vector{0, 1}},
			},
			want: vectorstore.ErrDuplicateID,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := vectorstore.ClusterItems(tc.items, 0.5)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ClusterItems(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}
