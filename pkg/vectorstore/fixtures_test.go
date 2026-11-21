package vectorstore_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

const fixtureMetaKey = "label"

// item builds an Item whose Metadata is unique to its ID, so a result that
// pairs the right ID with another item's vector or metadata is detectable.
func item(id string, components ...float64) vectorstore.Item {
	return vectorstore.Item{
		ID:       id,
		Vector:   vectorstore.Vector(components),
		Metadata: map[string]string{fixtureMetaKey: "meta-" + id},
	}
}

// snapshotByID deep-copies items keyed by ID. Take it before the call under
// test so expectations share no memory with anything the call may mutate.
func snapshotByID(t *testing.T, items []vectorstore.Item) map[string]vectorstore.Item {
	t.Helper()
	out := make(map[string]vectorstore.Item, len(items))
	for _, it := range items {
		out[it.ID] = vectorstore.Item{
			ID:       it.ID,
			Vector:   slices.Clone(it.Vector),
			Metadata: maps.Clone(it.Metadata),
		}
	}
	return out
}

// wantClusters expands groups of member IDs into full clusters using the
// snapshot payloads. It returns nil for no groups.
func wantClusters(t *testing.T, snapshot map[string]vectorstore.Item, groups [][]string) []vectorstore.Cluster {
	t.Helper()
	if len(groups) == 0 {
		return nil
	}
	out := make([]vectorstore.Cluster, len(groups))
	for i, ids := range groups {
		members := make([]vectorstore.Item, len(ids))
		for j, id := range ids {
			members[j] = snapshotItem(t, snapshot, id)
		}
		out[i] = vectorstore.Cluster{Items: members}
	}
	return out
}

// pair is an expected duplicate: member IDs plus the exact cosine score.
type pair struct {
	a, b  string
	score float64
}

// wantDuplicates expands expected pairs into full duplicates using the
// snapshot payloads. It returns nil for no pairs.
func wantDuplicates(t *testing.T, snapshot map[string]vectorstore.Item, pairs []pair) []vectorstore.Duplicate {
	t.Helper()
	if len(pairs) == 0 {
		return nil
	}
	out := make([]vectorstore.Duplicate, len(pairs))
	for i, p := range pairs {
		out[i] = vectorstore.Duplicate{
			A:     snapshotItem(t, snapshot, p.a),
			B:     snapshotItem(t, snapshot, p.b),
			Score: p.score,
		}
	}
	return out
}

func snapshotItem(t *testing.T, snapshot map[string]vectorstore.Item, id string) vectorstore.Item {
	t.Helper()
	it, ok := snapshot[id]
	if !ok {
		t.Fatalf("expected item %q is not in the fixture", id)
	}
	return it
}
