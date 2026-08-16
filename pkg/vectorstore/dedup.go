package vectorstore

import (
	"context"
	"fmt"
	"sort"
)

// Duplicate is a pair of items whose cosine similarity meets a threshold. A is
// always the item with the smaller ID, so each unordered pair appears once.
type Duplicate struct {
	// A is the item with the smaller ID.
	A Item
	// B is the item with the larger ID.
	B Item
	// Score is the cosine similarity between A and B.
	Score float64
}

// FindDuplicates returns every pair of items whose cosine similarity is at
// least threshold. It is a pure function over the supplied items rather than a
// Store method, so persistent backends do not have to expose their entire
// contents to support deduplication.
//
// Pairs are deduplicated (each unordered pair appears once, with A.ID < B.ID)
// and the result is sorted by (A.ID, B.ID) for deterministic output. Every item
// is validated first: IDs must be non-empty and unique, and vectors must be
// valid and share one dimension (see the package errors). threshold must be
// within the cosine range [-1, 1].
func FindDuplicates(ctx context.Context, items []Item, threshold float64) ([]Duplicate, error) {
	if err := validateThreshold(threshold); err != nil {
		return nil, err
	}
	normalized, err := normalizeItems(items)
	if err != nil {
		return nil, err
	}

	var dups []Duplicate
	for i := range items {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("finding duplicates: %w", err)
		}
		for j := i + 1; j < len(items); j++ {
			score := dot(normalized[i], normalized[j])
			if score >= threshold {
				a, b := orderPair(items[i], items[j])
				dups = append(dups, Duplicate{A: copyItem(a), B: copyItem(b), Score: score})
			}
		}
	}

	sort.Slice(dups, func(x, y int) bool {
		if dups[x].A.ID != dups[y].A.ID {
			return dups[x].A.ID < dups[y].A.ID
		}
		return dups[x].B.ID < dups[y].B.ID
	})
	return dups, nil
}
