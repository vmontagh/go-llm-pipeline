package vectorstore

import (
	"fmt"
	"sort"
)

// Cluster is a group of items connected, directly or transitively, by
// pairwise cosine similarity at or above a threshold.
type Cluster struct {
	// Items are the cluster members, sorted by ascending ID.
	Items []Item
}

// ClusterItems groups items into clusters using single-linkage connected
// components: an edge exists between two items when their cosine similarity is
// at least threshold, and each connected component becomes one Cluster. This is
// deterministic and inspectable rather than an approximate or randomized
// algorithm.
//
// Items within a cluster are sorted by ID, and clusters are sorted by their
// first member's ID. threshold must be within the cosine range [-1, 1]. An
// item that is similar to no other item forms a singleton cluster.
func ClusterItems(items []Item, threshold float64) ([]Cluster, error) {
	if err := validateThreshold(threshold); err != nil {
		return nil, err
	}

	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			score, err := cosineSimilarity(items[i].Vector, items[j].Vector)
			if err != nil {
				return nil, fmt.Errorf("clustering items %q and %q: %w", items[i].ID, items[j].ID, err)
			}
			if score >= threshold {
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
	return clusters, nil
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
