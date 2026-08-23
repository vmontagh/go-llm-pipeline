package vectorstore

import (
	"fmt"
	"sort"
)

// Cluster is a group of items connected, directly or transitively, by
// single-linkage clustering: each item is joined to every other item within a
// distance threshold, and the transitive closure of those links is one cluster.
type Cluster struct {
	// Items are the cluster members, sorted by ascending ID.
	Items []Item
}

// Metric returns the distance between two items; smaller means more similar,
// and 0 means identical under the metric. Implementations may read any field of
// Item — a scalar timestamp, a coordinate pair, an embedding vector — so the
// clusterer is not tied to any single notion of distance. ClusterByDistance
// only ever calls a metric with items drawn from its own input and assumes it
// is symmetric, i.e. metric(a, b) == metric(b, a).
type Metric func(a, b Item) float64

// ClusterByDistance groups items via single-linkage connected components: an
// edge connects a and b when metric(a, b) <= maxDistance, and each connected
// component becomes one Cluster. Linkage is transitive, so a chain of items
// each within maxDistance of the next joins a single cluster even when its
// endpoints lie far apart. This is deterministic and inspectable rather than an
// approximate or randomized algorithm.
//
// The metric is caller-supplied, so ClusterByDistance is independent of any
// particular distance (cosine, time gap, geographic distance, ...). It does not
// assume items carry vectors: only IDs are validated here — they must be
// non-empty and unique (see the package errors) — and any metric-specific
// validation is the caller's responsibility.
//
// Items within a cluster are sorted by ID, and clusters are sorted by their
// first member's ID, so output is stable regardless of input order. An item
// within maxDistance of no other item forms a singleton cluster.
func ClusterByDistance(items []Item, metric Metric, maxDistance float64) ([]Cluster, error) {
	if err := validateUniqueIDs(items); err != nil {
		return nil, fmt.Errorf("clustering items: %w", err)
	}
	return clusterComponents(items, func(i, j int) bool {
		return metric(items[i], items[j]) <= maxDistance
	}), nil
}

// ClusterItems groups items into clusters using single-linkage connected
// components over cosine similarity: an edge exists between two items when their
// cosine similarity is at least threshold, and each connected component becomes
// one Cluster. It is a cosine convenience wrapper over [ClusterByDistance] using
// cosine distance (1 - cosine similarity) and maxDistance = 1 - threshold, so it
// shares that function's deterministic ordering.
//
// Items within a cluster are sorted by ID, and clusters are sorted by their
// first member's ID. Every item is validated first: IDs must be non-empty and
// unique, and vectors must be valid and share one dimension (see the package
// errors). threshold must be within the cosine range [-1, 1]. An item that is
// similar to no other item forms a singleton cluster.
func ClusterItems(items []Item, threshold float64) ([]Cluster, error) {
	if err := validateThreshold(threshold); err != nil {
		return nil, fmt.Errorf("clustering items: %w", err)
	}
	normalized, err := normalizeItems(items)
	if err != nil {
		return nil, fmt.Errorf("clustering items: %w", err)
	}

	// Index the pre-normalized unit vectors by ID so the cosine-distance metric
	// avoids re-normalizing on every pairwise comparison. IDs are unique here
	// because normalizeItems has already validated them.
	unit := make(map[string]Vector, len(items))
	for i, item := range items {
		unit[item.ID] = normalized[i]
	}
	cosineDistance := func(a, b Item) float64 {
		return 1 - cosine(unit[a.ID], unit[b.ID])
	}
	return ClusterByDistance(items, cosineDistance, 1-threshold)
}

// clusterComponents groups items into single-linkage connected components with a
// union-find (path compression via find). connected reports whether items i and
// j share an edge. It assumes items have already been validated, and returns
// clusters with members sorted by ID and clusters sorted by their first member's
// ID for deterministic, input-order-independent output.
func clusterComponents(items []Item, connected func(i, j int) bool) []Cluster {
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			if connected(i, j) {
				union(parent, i, j)
			}
		}
	}

	groups := make(map[int][]Item)
	for i := range items {
		root := find(parent, i)
		groups[root] = append(groups[root], copyItem(items[i]))
	}

	clusters := make([]Cluster, 0, len(groups))
	for _, members := range groups {
		sort.Slice(members, func(a, b int) bool { return members[a].ID < members[b].ID })
		clusters = append(clusters, Cluster{Items: members})
	}
	sort.Slice(clusters, func(a, b int) bool {
		return clusters[a].Items[0].ID < clusters[b].Items[0].ID
	})
	return clusters
}

// find returns the representative root of i, compressing the path along the
// way so later lookups are near-constant time.
func find(parent []int, i int) int {
	for parent[i] != i {
		parent[i] = parent[parent[i]]
		i = parent[i]
	}
	return i
}

// union merges the components containing a and b.
func union(parent []int, a, b int) {
	ra, rb := find(parent, a), find(parent, b)
	if ra != rb {
		parent[rb] = ra
	}
}
