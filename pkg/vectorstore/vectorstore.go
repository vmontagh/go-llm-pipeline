// Package vectorstore provides an optional, local-first, dependency-free
// toolkit for semantic similarity, deduplication, and basic clustering over
// caller-supplied embedding vectors.
//
// The package deliberately does not compute embeddings itself: callers own the
// choice of embedding model and inject the resulting vectors. This keeps the
// core module dependency-light. The MVP ships an in-memory implementation
// ([MemoryStore]); a persistent implementation can be added later behind the
// same method set.
//
// All operations run fully offline. Similarity is cosine similarity over
// normalized vectors, so scores lie in the range [-1, 1] where 1 is identical
// direction, 0 is orthogonal, and -1 is opposite.
package vectorstore

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
)

// Sentinel errors returned by the package. Callers use errors.Is to detect a
// category without inspecting error text.
var (
	// ErrEmptyID is returned when an Item has no identifier.
	ErrEmptyID = errors.New("empty id")
	// ErrDuplicateID is returned when a slice of items contains the same ID
	// more than once, which would make pair and cluster output ambiguous.
	ErrDuplicateID = errors.New("duplicate id")
	// ErrEmptyVector is returned when a vector has no components.
	ErrEmptyVector = errors.New("empty vector")
	// ErrInvalidVector is returned when a vector contains a non-finite
	// component or has zero magnitude, for which cosine similarity is
	// undefined.
	ErrInvalidVector = errors.New("invalid vector")
	// ErrDimensionMismatch is returned when a vector's dimension differs from
	// the dimension the store or comparison expects.
	ErrDimensionMismatch = errors.New("dimension mismatch")
	// ErrInvalidLimit is returned when a nearest-neighbor limit is not
	// positive.
	ErrInvalidLimit = errors.New("invalid limit")
	// ErrInvalidThreshold is returned when a similarity threshold is NaN or
	// falls outside the cosine range [-1, 1].
	ErrInvalidThreshold = errors.New("invalid threshold")
)

// Vector is an embedding vector. Callers pass raw vectors; stores and helpers
// normalize them internally as needed for similarity scoring.
type Vector []float64

// Item is a single indexed embedding together with caller-owned metadata. The
// ID must be stable and unique within a store; adding an Item with an existing
// ID replaces the prior entry.
type Item struct {
	// ID is the stable, unique identifier for the embedding.
	ID string
	// Vector is the embedding itself.
	Vector Vector
	// Metadata is optional caller-owned data carried alongside the vector. It
	// is copied on input and output so stored data cannot be mutated through a
	// shared reference.
	Metadata map[string]string
}

// Neighbor is a single nearest-neighbor result. Score is the cosine similarity
// between the query vector and Item's vector.
type Neighbor struct {
	// Item is the matched, defensively copied item.
	Item Item
	// Score is the cosine similarity in [-1, 1], higher is more similar.
	Score float64
}

// normalize returns a unit-length copy of v. It rejects empty vectors, vectors
// with non-finite components, and zero-magnitude vectors, for which cosine
// similarity is undefined.
//
// Components are scaled by the largest absolute value before the sum of squares
// is formed. This keeps the intermediate magnitude near 1, so very large
// components do not overflow to +Inf (which would cache a valid vector as all
// zeros) and very small components do not underflow to 0.
func normalize(v Vector) (Vector, error) {
	if len(v) == 0 {
		return nil, ErrEmptyVector
	}
	var maxAbs float64
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%w: non-finite component", ErrInvalidVector)
		}
		if a := math.Abs(x); a > maxAbs {
			maxAbs = a
		}
	}
	if maxAbs == 0 {
		return nil, fmt.Errorf("%w: zero magnitude", ErrInvalidVector)
	}
	var sum float64
	for _, x := range v {
		scaled := x / maxAbs
		sum += scaled * scaled
	}
	root := math.Sqrt(sum)
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = (x / maxAbs) / root
	}
	return out, nil
}

// dot returns the dot product of two equal-length vectors. Callers guarantee
// equal length via dimension enforcement.
func dot(a, b Vector) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// cosineEpsilon is how close a raw dot product must be to an endpoint before it
// is snapped there. Independently normalizing two mathematically identical
// vectors can land a few ULPs off ±1 (for example, identical {1, 1} vectors
// score 0.9999999999999998), which would wrongly exclude them from an
// endpoint-threshold comparison.
const cosineEpsilon = 1e-9

// cosine returns the cosine similarity of two unit vectors: their dot product
// clamped to [-1, 1] and snapped to an exact endpoint when within
// cosineEpsilon. This keeps threshold comparisons well-behaved at the exact
// endpoints ±1, so identical vectors score exactly 1 and opposite vectors
// exactly -1.
func cosine(a, b Vector) float64 {
	switch s := dot(a, b); {
	case s > 1-cosineEpsilon:
		return 1
	case s < -1+cosineEpsilon:
		return -1
	default:
		return s
	}
}

// normalizeItems validates every item and returns their unit-length vectors,
// positionally aligned with items. It enforces non-empty, unique IDs, valid
// vectors, and a single shared dimension, so batch helpers such as
// [FindDuplicates] and [ClusterItems] apply the same validation the store does
// regardless of how many items are supplied. Normalizing once here also avoids
// re-normalizing the same vector across the pairwise comparison loops.
func normalizeItems(items []Item) ([]Vector, error) {
	normalized := make([]Vector, len(items))
	seen := make(map[string]struct{}, len(items))
	dim := 0
	for i, item := range items {
		if item.ID == "" {
			return nil, ErrEmptyID
		}
		if _, ok := seen[item.ID]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateID, item.ID)
		}
		seen[item.ID] = struct{}{}
		nv, err := normalize(item.Vector)
		if err != nil {
			return nil, fmt.Errorf("item %q: %w", item.ID, err)
		}
		if dim == 0 {
			dim = len(item.Vector)
		} else if len(item.Vector) != dim {
			return nil, fmt.Errorf("item %q: %w: got %d, want %d", item.ID, ErrDimensionMismatch, len(item.Vector), dim)
		}
		normalized[i] = nv
	}
	return normalized, nil
}

// validateThreshold rejects thresholds that are NaN or outside the cosine
// range [-1, 1].
func validateThreshold(threshold float64) error {
	if math.IsNaN(threshold) || threshold < -1 || threshold > 1 {
		return fmt.Errorf("%w: %v", ErrInvalidThreshold, threshold)
	}
	return nil
}

// copyItem returns a deep copy of item so stored and returned data cannot be
// mutated through a shared slice or map reference.
func copyItem(item Item) Item {
	return Item{
		ID:       item.ID,
		Vector:   slices.Clone(item.Vector),
		Metadata: maps.Clone(item.Metadata),
	}
}

// orderPair returns a and b ordered so the first has the smaller ID, giving
// deterministic pair output regardless of input order.
func orderPair(a, b Item) (low, high Item) {
	if a.ID <= b.ID {
		return a, b
	}
	return b, a
}
