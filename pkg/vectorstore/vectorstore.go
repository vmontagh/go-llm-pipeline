// Package vectorstore provides an optional, local-first, dependency-free
// toolkit for semantic similarity, deduplication, and basic clustering over
// caller-supplied embedding vectors.
//
// The package deliberately does not compute embeddings itself: callers own the
// choice of embedding model and inject the resulting vectors. This keeps the
// core module dependency-light and lets the same [Store] contract back several
// implementations. The MVP ships an in-memory implementation ([MemoryStore]);
// a persistent store can satisfy the same interface later without changing
// caller code.
//
// All operations run fully offline. Similarity is cosine similarity over
// normalized vectors, so scores lie in the range [-1, 1] where 1 is identical
// direction, 0 is orthogonal, and -1 is opposite.
package vectorstore

import (
	"context"
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

// Store indexes embeddings and answers nearest-neighbor queries. It is the
// abstraction that decouples callers from a particular storage backend, so an
// in-memory store today and a persistent store later are interchangeable.
type Store interface {
	// Add inserts item, replacing any existing item with the same ID. It
	// returns a sentinel-wrapped error for invalid input (see the package
	// errors).
	Add(ctx context.Context, item Item) error
	// Nearest returns up to limit neighbors ordered by descending Score, with
	// ties broken by ascending Item.ID. It returns ErrInvalidLimit when limit
	// is not positive and fewer than limit results when the store holds fewer
	// items.
	Nearest(ctx context.Context, vector Vector, limit int) ([]Neighbor, error)
}

// Embedder converts a caller value of type T into an embedding Vector. It is
// the injection seam for embedding models: the package intentionally provides
// no implementation so callers stay in control of the model and its
// dependencies, then feed the produced vectors into a [Store].
type Embedder[T any] interface {
	// Embed returns the embedding for value.
	Embed(ctx context.Context, value T) (Vector, error)
}

// normalize returns a unit-length copy of v. It rejects empty vectors, vectors
// with non-finite components, and zero-magnitude vectors, for which cosine
// similarity is undefined.
func normalize(v Vector) (Vector, error) {
	if len(v) == 0 {
		return nil, ErrEmptyVector
	}
	var sum float64
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, fmt.Errorf("%w: non-finite component", ErrInvalidVector)
		}
		sum += x * x
	}
	mag := math.Sqrt(sum)
	if mag == 0 {
		return nil, fmt.Errorf("%w: zero magnitude", ErrInvalidVector)
	}
	out := make(Vector, len(v))
	for i, x := range v {
		out[i] = x / mag
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

// cosineSimilarity returns the cosine similarity between two raw vectors,
// validating that they share a dimension and are individually valid.
func cosineSimilarity(a, b Vector) (float64, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("%w: got %d and %d", ErrDimensionMismatch, len(a), len(b))
	}
	na, err := normalize(a)
	if err != nil {
		return 0, err
	}
	nb, err := normalize(b)
	if err != nil {
		return 0, err
	}
	return dot(na, nb), nil
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
