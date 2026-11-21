package vectorstore_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

const (
	// aliasThreshold links every fixture item, so each call returns all of them.
	aliasThreshold = 0.5
	// aliasMaxDistance is the ClusterByDistance analogue of aliasThreshold.
	aliasMaxDistance = 10.0
	// scribble overwrites returned vector components and metadata values.
	scribble = -42
)

// TestResultsDoNotAliasInput checks that each grouping function leaves its
// input as it was (no in-place normalization) and returns items that share no
// vector or metadata storage with the input.
func TestResultsDoNotAliasInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  func(context.Context, []vectorstore.Item) ([]vectorstore.Item, error)
	}{
		{name: "FindDuplicates", run: duplicateMembers},
		{name: "ClusterItems", run: clusterMembers},
		{name: "ClusterByDistance", run: distanceClusterMembers},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Non-unit vectors make any in-place normalization visible.
			items := []vectorstore.Item{item("a", 3, 4), item("b", 6, 8), item("c", 5, 5)}
			want := snapshotByID(t, items)

			got, err := tc.run(t.Context(), items)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if len(got) == 0 {
				t.Fatalf("%s returned no items; the fixture must link them", tc.name)
			}
			assertUnchanged(t, "after the call", want, items)

			for _, it := range got {
				it.Vector[0] = scribble
				it.Metadata[fixtureMetaKey] = "scribbled"
			}
			assertUnchanged(t, "after writing to the results", want, items)
		})
	}
}

func duplicateMembers(ctx context.Context, items []vectorstore.Item) ([]vectorstore.Item, error) {
	dups, err := vectorstore.FindDuplicates(ctx, items, aliasThreshold)
	out := make([]vectorstore.Item, 0, 2*len(dups))
	for _, d := range dups {
		out = append(out, d.A, d.B)
	}
	return out, err
}

func clusterMembers(_ context.Context, items []vectorstore.Item) ([]vectorstore.Item, error) {
	clusters, err := vectorstore.ClusterItems(items, aliasThreshold)
	return flatten(clusters), err
}

func distanceClusterMembers(_ context.Context, items []vectorstore.Item) ([]vectorstore.Item, error) {
	clusters, err := vectorstore.ClusterByDistance(items, axisDistance, aliasMaxDistance)
	return flatten(clusters), err
}

func flatten(clusters []vectorstore.Cluster) []vectorstore.Item {
	var out []vectorstore.Item
	for _, c := range clusters {
		out = append(out, c.Items...)
	}
	return out
}

func assertUnchanged(t *testing.T, when string, want map[string]vectorstore.Item, items []vectorstore.Item) {
	t.Helper()
	for _, it := range items {
		if diff := cmp.Diff(want[it.ID], it); diff != "" {
			t.Errorf("input item %q changed %s (-want +got):\n%s", it.ID, when, diff)
		}
	}
}
