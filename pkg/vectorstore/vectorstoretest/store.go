package vectorstoretest

import (
	"context"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// Store is the contract a vector store must satisfy to pass the conformance
// suite. It is defined here, at the consumer, rather than in pkg/vectorstore,
// because this harness is its only consumer.
//
// An implementation must uphold the full drop-in contract that
// [RunStoreConformance] exercises:
//
//   - Add inserts item, replacing any existing item with the same Item.ID
//     (upsert). It reports ErrEmptyID for an empty ID, ErrEmptyVector for a
//     vector with no components, and ErrInvalidVector for a non-finite or
//     zero-magnitude vector.
//   - The store pins its vector dimension on the first successful Add and
//     returns ErrDimensionMismatch for any later Add or Nearest whose vector
//     has a different dimension.
//   - Nearest returns up to limit neighbors ordered by descending cosine
//     similarity, breaking ties by ascending Item.ID. It validates the query
//     vector the same way Add validates item vectors: ErrEmptyVector for an
//     empty query, ErrInvalidVector for a non-finite or zero-magnitude query,
//     and ErrDimensionMismatch when the query dimension differs from the pinned
//     store dimension. It returns ErrInvalidLimit when limit is not positive.
//   - On an empty store a valid Nearest returns no neighbors and a nil error;
//     it must not report an ErrNotFound-style error.
//   - Items are defensively copied on input and output, so mutating a caller's
//     input after Add or a returned Neighbor.Item afterward must not affect
//     stored state.
//   - If ctx is cancelled or its deadline is exceeded, Add and Nearest return
//     an error wrapping ctx.Err(); callers can detect it with
//     errors.Is(err, context.Canceled) or errors.Is(err, context.DeadlineExceeded),
//     matching ctx.Err().
type Store interface {
	// Add inserts item, replacing any existing item with the same ID.
	Add(ctx context.Context, item vectorstore.Item) error
	// Nearest returns up to limit neighbors ordered by descending cosine
	// similarity.
	Nearest(ctx context.Context, vector vectorstore.Vector, limit int) ([]vectorstore.Neighbor, error)
}
