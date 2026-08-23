// Package vectorstoretest provides a reusable conformance harness that verifies
// any [Store] implementation against the shared behavioral contract of the
// vectorstore package.
//
// It mirrors standard-library patterns such as testing/fstest: a backend author
// supplies a factory and [RunStoreConformance] exercises the contract, so every
// Store implementation — the in-memory store today and a persistent backend
// later — is checked against one authoritative specification instead of a
// separately maintained copy of the same tests. The [Store] interface lives
// here, at the consumer, rather than in pkg/vectorstore.
package vectorstoretest

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// approxScore bounds the floating-point slack allowed when comparing cosine
// similarity scores, which can differ by a few ULPs across independent
// normalization.
const approxScore = 1e-9

// Fixture item IDs reused across subtests, named so the same literal is not
// repeated between the seeded items and the expected result.
const (
	idAlpha   = "alpha"
	idEast    = "east"
	idGood    = "good"
	idNorth   = "north"
	idResized = "resized"
	idSettle  = "settle"
	idWest    = "west"
	idZebra   = "zebra"
)

// RunStoreConformance runs the shared [Store] behavioral contract against a
// store produced by newStore. newStore must return a fresh, empty store each
// call and may use tb.TempDir()/tb.Cleanup for resources.
//
// The suite runs its subtests in parallel, so newStore is called concurrently:
// it must be safe for concurrent use. Each call must return an independent,
// fresh store, and the factory must not share mutable state across calls
// without synchronization, so consumers do not introduce data races through
// shared factory state.
func RunStoreConformance(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Helper()

	t.Run("AddNearestRoundTrip", func(t *testing.T) { testRoundTrip(t, newStore) })
	t.Run("NearestOrdering", func(t *testing.T) { testNearestOrdering(t, newStore) })
	t.Run("AddValidation", func(t *testing.T) { testAddValidation(t, newStore) })
	t.Run("DimensionPinning", func(t *testing.T) { testDimensionPinning(t, newStore) })
	t.Run("NearestErrors", func(t *testing.T) { testNearestErrors(t, newStore) })
	t.Run("Upsert", func(t *testing.T) { testUpsert(t, newStore) })
	t.Run("DefensiveCopy", func(t *testing.T) { testDefensiveCopy(t, newStore) })
	t.Run("ContextCancellation", func(t *testing.T) { testContextCancellation(t, newStore) })
	t.Run("EmptyStore", func(t *testing.T) { testEmptyStore(t, newStore) })
}

// testRoundTrip checks that an added item, including its metadata, is returned
// unchanged by a Nearest query for the same vector and scores as identical.
func testRoundTrip(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	tests := []struct {
		name      string
		item      vectorstore.Item
		query     vectorstore.Vector
		wantScore float64
	}{
		{
			name:      "vector and metadata round-trip",
			item:      vectorstore.Item{ID: "a", Vector: vectorstore.Vector{3, 4}, Metadata: map[string]string{"k": "v"}},
			query:     vectorstore.Vector{3, 4},
			wantScore: 1.0,
		},
		{
			name:      "three dimensional round trip",
			item:      vectorstore.Item{ID: "b", Vector: vectorstore.Vector{2, 3, 6}, Metadata: map[string]string{"k": "v"}},
			query:     vectorstore.Vector{2, 3, 6},
			wantScore: 1.0,
		},
		// The validation contract rejects only empty, non-finite, and
		// zero-magnitude vectors, so a one-component nonzero vector is valid and
		// must round-trip; this pins the lower dimensional boundary and fails a
		// backend that wrongly imposes a minimum dimension of 2.
		{
			name:      "one dimensional round trip",
			item:      vectorstore.Item{ID: "c", Vector: vectorstore.Vector{5}, Metadata: map[string]string{"k": "v"}},
			query:     vectorstore.Vector{5},
			wantScore: 1.0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			mustAdd(t, s, tc.item)

			got, err := s.Nearest(t.Context(), tc.query, 1)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.query, 1, err)
			}
			if len(got) != 1 {
				t.Fatalf("Nearest(%v, %d) returned %d neighbors, want 1", tc.query, 1, len(got))
			}
			if diff := cmp.Diff(tc.item, got[0].Item); diff != "" {
				t.Errorf("Nearest(%v, %d) round-trip item mismatch (-want +got):\n%s", tc.query, 1, diff)
			}
			if diff := cmp.Diff(tc.wantScore, got[0].Score, cmpopts.EquateApprox(0, approxScore)); diff != "" {
				t.Errorf("Nearest(%v, %d) round-trip score mismatch (-want +got):\n%s", tc.query, 1, diff)
			}
		})
	}
}

// testNearestOrdering checks that Nearest orders neighbors by descending cosine
// similarity, breaks ties by ascending Item.ID, and honors limit.
func testNearestOrdering(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	tests := []struct {
		name       string
		items      []vectorstore.Item
		query      vectorstore.Vector
		limit      int
		wantItems  []vectorstore.Item
		wantScores []float64
	}{
		{
			name: "descending similarity",
			items: []vectorstore.Item{
				{ID: idEast, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idEast}},
				{ID: idNorth, Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"k": idNorth}},
				{ID: idWest, Vector: vectorstore.Vector{-1, 0}, Metadata: map[string]string{"k": idWest}},
			},
			query: vectorstore.Vector{2, 0},
			limit: 3,
			wantItems: []vectorstore.Item{
				{ID: idEast, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idEast}},
				{ID: idNorth, Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"k": idNorth}},
				{ID: idWest, Vector: vectorstore.Vector{-1, 0}, Metadata: map[string]string{"k": idWest}},
			},
			wantScores: []float64{1, 0, -1},
		},
		{
			name: "ties broken by ascending id",
			items: []vectorstore.Item{
				{ID: idZebra, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idZebra}},
				{ID: idAlpha, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idAlpha}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 2,
			wantItems: []vectorstore.Item{
				{ID: idAlpha, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idAlpha}},
				{ID: idZebra, Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": idZebra}},
			},
			wantScores: []float64{1, 1},
		},
		{
			name: "limit truncates results",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "a"}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"k": "b"}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 1,
			wantItems: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "a"}},
			},
			wantScores: []float64{1},
		},
		{
			name: "limit larger than store returns all",
			items: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "a"}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"k": "b"}},
			},
			query: vectorstore.Vector{1, 0},
			limit: 10,
			wantItems: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"k": "a"}},
				{ID: "b", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"k": "b"}},
			},
			wantScores: []float64{1, 0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			mustAdd(t, s, tc.items...)

			got, err := s.Nearest(t.Context(), tc.query, tc.limit)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.query, tc.limit, err)
			}
			if diff := cmp.Diff(tc.wantItems, neighborItems(got)); diff != "" {
				t.Errorf("Nearest(%v, %d) item order/payload mismatch (-want +got):\n%s", tc.query, tc.limit, diff)
			}
			if diff := cmp.Diff(tc.wantScores, neighborScores(got), cmpopts.EquateApprox(0, approxScore)); diff != "" {
				t.Errorf("Nearest(%v, %d) score mismatch (-want +got):\n%s", tc.query, tc.limit, diff)
			}
		})
	}
}

// testAddValidation checks that Add rejects invalid items with the matching
// sentinel error and that a rejected Add is a no-op: it persists nothing and
// pins no dimension. Each row settles a valid item afterward and asserts it is
// the sole stored content.
func testAddValidation(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	tests := []struct {
		name    string
		seed    []vectorstore.Item
		item    vectorstore.Item
		want    error
		settle  vectorstore.Item
		probe   vectorstore.Vector
		wantIDs []string
	}{
		{
			name:    "empty id",
			item:    vectorstore.Item{Vector: vectorstore.Vector{1, 0}},
			want:    vectorstore.ErrEmptyID,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name:    "empty vector",
			item:    vectorstore.Item{ID: "a"},
			want:    vectorstore.ErrEmptyVector,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name: "non-finite component",
			// Invalid value at index 1 (not 0) exercises whole-vector
			// validation: a backend that checks only the first component must
			// still reject this.
			item:    vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, math.NaN()}},
			want:    vectorstore.ErrInvalidVector,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name:    "positive infinity component",
			item:    vectorstore.Item{ID: "a", Vector: vectorstore.Vector{math.Inf(1), 0}},
			want:    vectorstore.ErrInvalidVector,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name:    "negative infinity component",
			item:    vectorstore.Item{ID: "a", Vector: vectorstore.Vector{math.Inf(-1), 0}},
			want:    vectorstore.ErrInvalidVector,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name:    "zero magnitude",
			item:    vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 0}},
			want:    vectorstore.ErrInvalidVector,
			settle:  vectorstore.Item{ID: idSettle, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idSettle},
		},
		{
			name:    "dimension mismatch",
			seed:    []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}},
			item:    vectorstore.Item{ID: "b", Vector: vectorstore.Vector{1, 0, 0}},
			want:    vectorstore.ErrDimensionMismatch,
			probe:   vectorstore.Vector{1, 0},
			wantIDs: []string{"a"},
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			mustAdd(t, s, tc.seed...)

			err := s.Add(t.Context(), tc.item)
			if diff := cmp.Diff(tc.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Add(%+v) error mismatch (-want +got):\n%s", tc.item, diff)
			}

			if tc.settle.ID != "" {
				mustAdd(t, s, tc.settle)
			}
			got, err := s.Nearest(t.Context(), tc.probe, 2)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.probe, 2, err)
			}
			if diff := cmp.Diff(tc.wantIDs, neighborIDs(got)); diff != "" {
				t.Errorf("store state after rejected Add(%+v) (-want +got):\n%s", tc.item, diff)
			}
		})
	}
}

// testDimensionPinning checks that the store pins its dimension on the first
// SUCCESSFUL Add: a failed first Add and an empty-store Nearest must not pin a
// dimension, and a same-ID upsert with a mismatched dimension must be rejected
// while leaving the original entry intact.
func testDimensionPinning(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	addStep := func(item vectorstore.Item) func(context.Context, Store) error {
		return func(ctx context.Context, s Store) error { return s.Add(ctx, item) }
	}
	nearestStep := func(query vectorstore.Vector, limit int) func(context.Context, Store) error {
		return func(ctx context.Context, s Store) error {
			_, err := s.Nearest(ctx, query, limit)
			return err
		}
	}

	type step struct {
		name    string
		op      func(ctx context.Context, s Store) error
		wantErr error
	}
	tests := []struct {
		name      string
		steps     []step
		probe     vectorstore.Vector
		wantCount int
		wantItem  vectorstore.Item
	}{
		{
			name: "failed first add does not pin dimension",
			steps: []step{
				{name: "invalid dim-3 add", op: addStep(vectorstore.Item{ID: "bad", Vector: vectorstore.Vector{0, 0, 0}}), wantErr: vectorstore.ErrInvalidVector},
				{name: "dim-2 add after failed add", op: addStep(vectorstore.Item{ID: idGood, Vector: vectorstore.Vector{1, 0}})},
			},
			probe:     vectorstore.Vector{1, 0},
			wantCount: 1,
			wantItem:  vectorstore.Item{ID: idGood, Vector: vectorstore.Vector{1, 0}},
		},
		{
			name: "empty-store query does not pin dimension",
			steps: []step{
				{name: "dim-3 query on empty store", op: nearestStep(vectorstore.Vector{1, 0, 0}, 1)},
				{name: "dim-2 add after empty-store query", op: addStep(vectorstore.Item{ID: idGood, Vector: vectorstore.Vector{1, 0}})},
			},
			probe:     vectorstore.Vector{1, 0},
			wantCount: 1,
			wantItem:  vectorstore.Item{ID: idGood, Vector: vectorstore.Vector{1, 0}},
		},
		{
			name: "mismatched same-id upsert is rejected and preserves original",
			steps: []step{
				{name: "seed dim-2", op: addStep(vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}})},
				{name: "mismatched dim-3 upsert", op: addStep(vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 1, 0}}), wantErr: vectorstore.ErrDimensionMismatch},
			},
			probe:     vectorstore.Vector{1, 0},
			wantCount: 1,
			wantItem:  vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			for _, st := range tc.steps {
				err := st.op(t.Context(), s)
				if diff := cmp.Diff(st.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("step %q error mismatch (-want +got):\n%s", st.name, diff)
				}
			}

			if got := countItems(t, s, tc.probe, 2); got != tc.wantCount {
				t.Fatalf("item count = %d, want %d", got, tc.wantCount)
			}

			got, err := s.Nearest(t.Context(), tc.probe, 1)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.probe, 1, err)
			}
			if len(got) != 1 {
				t.Fatalf("Nearest(%v, %d) returned %d neighbors, want 1", tc.probe, 1, len(got))
			}
			if diff := cmp.Diff(tc.wantItem, got[0].Item); diff != "" {
				t.Errorf("Nearest(%v, %d) stored item mismatch (-want +got):\n%s", tc.probe, 1, diff)
			}
		})
	}
}

// testNearestErrors checks that Nearest rejects invalid limits, invalid query
// vectors, and mismatched query dimensions with the matching sentinel error.
func testNearestErrors(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	seed := []vectorstore.Item{{ID: "a", Vector: vectorstore.Vector{1, 0}}}
	tests := []struct {
		name  string
		seed  bool
		query vectorstore.Vector
		limit int
		want  error
	}{
		// Seeded rows run against a store with a pinned dimension.
		{name: "zero limit", seed: true, query: vectorstore.Vector{1, 0}, limit: 0, want: vectorstore.ErrInvalidLimit},
		{name: "negative limit", seed: true, query: vectorstore.Vector{1, 0}, limit: -1, want: vectorstore.ErrInvalidLimit},
		{name: "dimension mismatch query", seed: true, query: vectorstore.Vector{1, 0, 0}, limit: 1, want: vectorstore.ErrDimensionMismatch},
		// The query is validated through Normalize before the dimension check,
		// so an empty query reports ErrEmptyVector and a non-finite or
		// zero-magnitude query reports ErrInvalidVector even on a seeded store.
		{name: "empty query", seed: true, query: vectorstore.Vector{}, limit: 1, want: vectorstore.ErrEmptyVector},
		// NaN at index 1 (not 0) exercises whole-query validation; Normalize
		// runs before the dimension check, so this still reports
		// ErrInvalidVector on the seeded dim-2 store.
		{name: "non-finite query", seed: true, query: vectorstore.Vector{0, math.NaN()}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "positive infinity query", seed: true, query: vectorstore.Vector{math.Inf(1), 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "negative infinity query", seed: true, query: vectorstore.Vector{math.Inf(-1), 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "zero-magnitude query", seed: true, query: vectorstore.Vector{0, 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		// Empty-store rows: limit and query are validated regardless of store
		// contents, before any dimension is pinned.
		{name: "zero limit empty store", query: vectorstore.Vector{1, 0}, limit: 0, want: vectorstore.ErrInvalidLimit},
		{name: "negative limit empty store", query: vectorstore.Vector{1, 0}, limit: -1, want: vectorstore.ErrInvalidLimit},
		{name: "empty query empty store", query: vectorstore.Vector{}, limit: 1, want: vectorstore.ErrEmptyVector},
		{name: "non-finite query empty store", query: vectorstore.Vector{math.NaN(), 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "positive infinity query empty store", query: vectorstore.Vector{math.Inf(1), 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "negative infinity query empty store", query: vectorstore.Vector{math.Inf(-1), 0}, limit: 1, want: vectorstore.ErrInvalidVector},
		{name: "zero-magnitude query empty store", query: vectorstore.Vector{0, 0}, limit: 1, want: vectorstore.ErrInvalidVector},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			if tc.seed {
				mustAdd(t, s, seed...)
			}

			_, err := s.Nearest(t.Context(), tc.query, tc.limit)
			if diff := cmp.Diff(tc.want, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Nearest(%v, %d) error mismatch (-want +got):\n%s", tc.query, tc.limit, diff)
			}
		})
	}
}

// testUpsert checks that adding the same ID twice replaces the prior entry
// rather than inserting a duplicate.
func testUpsert(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	tests := []struct {
		name      string
		adds      []vectorstore.Item
		probe     vectorstore.Vector
		wantCount int
		wantItem  vectorstore.Item
	}{
		{
			name: "second add with same id replaces prior entry",
			adds: []vectorstore.Item{
				{ID: "a", Vector: vectorstore.Vector{1, 0}, Metadata: map[string]string{"old": "1"}},
				{ID: "a", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"v": "2"}},
			},
			probe:     vectorstore.Vector{0, 1},
			wantCount: 1,
			wantItem:  vectorstore.Item{ID: "a", Vector: vectorstore.Vector{0, 1}, Metadata: map[string]string{"v": "2"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			mustAdd(t, s, tc.adds...)

			if got := countItems(t, s, tc.probe, 2); got != tc.wantCount {
				t.Fatalf("item count after upsert = %d, want %d", got, tc.wantCount)
			}

			got, err := s.Nearest(t.Context(), tc.probe, 1)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.probe, 1, err)
			}
			if len(got) != 1 {
				t.Fatalf("Nearest(%v, %d) returned %d neighbors, want 1", tc.probe, 1, len(got))
			}
			if diff := cmp.Diff(tc.wantItem, got[0].Item); diff != "" {
				t.Errorf("Nearest(%v, %d) upserted item mismatch (-want +got):\n%s", tc.probe, 1, diff)
			}
			if diff := cmp.Diff(1.0, got[0].Score, cmpopts.EquateApprox(0, approxScore)); diff != "" {
				t.Errorf("Nearest(%v, %d) upserted item score mismatch (-want +got):\n%s", tc.probe, 1, diff)
			}
		})
	}
}

// testDefensiveCopy checks that the store copies metadata and vectors on input
// and output, so callers cannot mutate stored state through a shared reference.
func testDefensiveCopy(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	// The stored item and the query share direction {3,4}, so a faithful copy
	// scores exactly 1.0. Each case corrupts a caller-visible alias, then the
	// shared re-query below checks the store kept its own copy: an aliased
	// store returns a different item and/or a score below 1.
	tests := []struct {
		name    string
		corrupt func(t *testing.T, s Store, in vectorstore.Item)
	}{
		{
			// Mutate the input's DIRECTION immediately after Add, before any
			// query, so a store that retained the caller vector for scoring
			// returns the wrong score. A collinear change would be invisible
			// to cosine similarity, so flip the direction to {0,1} (cos 0.8
			// against {3,4}).
			name: "input mutated after add",
			corrupt: func(_ *testing.T, _ Store, in vectorstore.Item) {
				in.Vector[0], in.Vector[1] = 0, 1
				in.Metadata["k"] = "mutated"
			},
		},
		{
			// Mutate a returned neighbor after a query; the store must not share
			// the memory backing its stored item.
			name: "output mutated after query",
			corrupt: func(t *testing.T, s Store, _ vectorstore.Item) {
				out, err := s.Nearest(t.Context(), vectorstore.Vector{3, 4}, 1)
				if err != nil {
					t.Fatalf("Nearest(%v, %d) = %v, want nil", vectorstore.Vector{3, 4}, 1, err)
				}
				if len(out) != 1 {
					t.Fatalf("Nearest(%v, %d) returned %d neighbors, want 1", vectorstore.Vector{3, 4}, 1, len(out))
				}
				// Guard against a non-conforming backend: a malformed neighbor
				// (empty vector or nil metadata) would otherwise panic here
				// rather than fail with an actionable message (§3.7).
				if got := out[0].Item; len(got.Vector) == 0 || got.Metadata == nil {
					t.Fatalf("Nearest(%v, %d) returned malformed neighbor %+v, want a non-empty vector and non-nil metadata", vectorstore.Vector{3, 4}, 1, got)
				}
				out[0].Item.Vector[0] = 999
				out[0].Item.Metadata["k"] = "changed"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			// A non-unit vector makes in-place normalization observable:
			// normalizing {3,4} changes it, whereas an already-unit vector
			// would hide a backend that mutates the caller's slice during Add.
			in := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{3, 4}, Metadata: map[string]string{"k": "v"}}
			mustAdd(t, s, in)

			if diff := cmp.Diff(vectorstore.Vector{3, 4}, in.Vector); diff != "" {
				t.Errorf("Add mutated caller vector in place (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(map[string]string{"k": "v"}, in.Metadata); diff != "" {
				t.Errorf("Add mutated caller metadata in place (-want +got):\n%s", diff)
			}

			tc.corrupt(t, s, in)

			// After the caller-side mutation the store must still hold the
			// original item and score it 1.0 against the original query.
			got, err := s.Nearest(t.Context(), vectorstore.Vector{3, 4}, 1)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", vectorstore.Vector{3, 4}, 1, err)
			}
			if len(got) != 1 {
				t.Fatalf("Nearest(%v, %d) returned %d neighbors, want 1", vectorstore.Vector{3, 4}, 1, len(got))
			}
			want := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{3, 4}, Metadata: map[string]string{"k": "v"}}
			if diff := cmp.Diff(want, got[0].Item); diff != "" {
				t.Errorf("stored item mutated via shared reference (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(1.0, got[0].Score, cmpopts.EquateApprox(0, approxScore)); diff != "" {
				t.Errorf("stored scoring vector mutated via shared reference: score (-want +got):\n%s", diff)
			}
		})
	}
}

// testContextCancellation checks that a context which is canceled or whose
// deadline has passed makes Add and Nearest return an error wrapping ctx.Err(),
// including on an empty store where Nearest scans nothing. It also verifies that
// a failed Add is a no-op that pins no dimension: a differently sized valid Add
// still succeeds afterward, and the store's sole contents match expectations.
func testContextCancellation(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	item := vectorstore.Item{ID: "a", Vector: vectorstore.Vector{1, 0}}
	addOp := func(ctx context.Context, s Store) error {
		return s.Add(ctx, item)
	}
	nearestOp := func(ctx context.Context, s Store) error {
		_, err := s.Nearest(ctx, vectorstore.Vector{1, 0}, 1)
		return err
	}

	tests := []struct {
		name    string
		seed    bool
		ctxFn   func(tb testing.TB) context.Context
		op      func(ctx context.Context, s Store) error
		wantErr error
		settle  vectorstore.Item
		probe   vectorstore.Vector
		wantIDs []string
	}{
		{
			name:    "add canceled",
			ctxFn:   canceledContext,
			op:      addOp,
			wantErr: context.Canceled,
			settle:  vectorstore.Item{ID: idResized, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idResized},
		},
		{
			name:    "nearest canceled",
			seed:    true,
			ctxFn:   canceledContext,
			op:      nearestOp,
			wantErr: context.Canceled,
			probe:   vectorstore.Vector{1, 0},
			wantIDs: []string{"a"},
		},
		{
			name:    "nearest canceled empty store",
			ctxFn:   canceledContext,
			op:      nearestOp,
			wantErr: context.Canceled,
			probe:   vectorstore.Vector{1, 0},
			wantIDs: nil,
		},
		{
			name:    "add deadline exceeded",
			ctxFn:   expiredContext,
			op:      addOp,
			wantErr: context.DeadlineExceeded,
			settle:  vectorstore.Item{ID: idResized, Vector: vectorstore.Vector{1, 0, 0}},
			probe:   vectorstore.Vector{1, 0, 0},
			wantIDs: []string{idResized},
		},
		{
			name:    "nearest deadline exceeded",
			seed:    true,
			ctxFn:   expiredContext,
			op:      nearestOp,
			wantErr: context.DeadlineExceeded,
			probe:   vectorstore.Vector{1, 0},
			wantIDs: []string{"a"},
		},
		{
			name:    "nearest deadline exceeded empty store",
			ctxFn:   expiredContext,
			op:      nearestOp,
			wantErr: context.DeadlineExceeded,
			probe:   vectorstore.Vector{1, 0},
			wantIDs: nil,
		},
	}

	for i := range tests {
		tc := &tests[i]
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			if tc.seed {
				mustAdd(t, s, item)
			}
			err := tc.op(tc.ctxFn(t), s)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s error mismatch (-want +got):\n%s", tc.name, diff)
			}

			// A failed Add must persist nothing and pin no dimension, so a
			// differently sized valid Add still succeeds; read the sole
			// contents back with a live context to confirm.
			if tc.settle.ID != "" {
				mustAdd(t, s, tc.settle)
			}
			got, err := s.Nearest(t.Context(), tc.probe, 2)
			if err != nil {
				t.Fatalf("%s: Nearest(%v, %d) = %v, want nil", tc.name, tc.probe, 2, err)
			}
			if diff := cmp.Diff(tc.wantIDs, neighborIDs(got)); diff != "" {
				t.Errorf("%s store state (-want +got):\n%s", tc.name, diff)
			}
		})
	}
}

// testEmptyStore checks that Nearest on an empty store returns no neighbors and
// no error.
func testEmptyStore(t *testing.T, newStore func(tb testing.TB) Store) {
	t.Parallel()

	tests := []struct {
		name    string
		query   vectorstore.Vector
		limit   int
		wantLen int
	}{
		{
			name:  "nearest returns no neighbors",
			query: vectorstore.Vector{1, 0},
			limit: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			got, err := s.Nearest(t.Context(), tc.query, tc.limit)
			if err != nil {
				t.Fatalf("Nearest(%v, %d) = %v, want nil", tc.query, tc.limit, err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("Nearest(%v, %d) on empty store = %d neighbors, want %d", tc.query, tc.limit, len(got), tc.wantLen)
			}
		})
	}
}

// mustAdd adds every item to s, failing the test on the first error.
func mustAdd(tb testing.TB, s Store, items ...vectorstore.Item) {
	tb.Helper()
	for _, item := range items {
		if err := s.Add(tb.Context(), item); err != nil {
			tb.Fatalf("Add(%q) = %v, want nil", item.ID, err)
		}
	}
}

// countItems reports how many items s holds by querying up to limit neighbors
// for a dimension-compatible probe vector, using only the Store interface.
// limit must be at least the number of stored items the caller wants to
// observe; a small bound avoids provoking huge preallocations in a backend
// that sizes results from limit.
func countItems(tb testing.TB, s Store, probe vectorstore.Vector, limit int) int {
	tb.Helper()
	got, err := s.Nearest(tb.Context(), probe, limit)
	if err != nil {
		tb.Fatalf("Nearest(%v, %d) = %v, want nil", probe, limit, err)
	}
	return len(got)
}

// neighborIDs projects neighbors to their IDs for order-sensitive comparison.
func neighborIDs(neighbors []vectorstore.Neighbor) []string {
	if len(neighbors) == 0 {
		return nil
	}
	out := make([]string, len(neighbors))
	for i, n := range neighbors {
		out[i] = n.Item.ID
	}
	return out
}

// neighborScores projects neighbors to their scores for score comparison.
func neighborScores(neighbors []vectorstore.Neighbor) []float64 {
	if len(neighbors) == 0 {
		return nil
	}
	out := make([]float64, len(neighbors))
	for i, n := range neighbors {
		out[i] = n.Score
	}
	return out
}

// neighborItems projects neighbors to their items for full order-sensitive
// payload comparison, catching a backend that reuses a scan buffer and leaves
// every neighbor's item aliased to the last row.
func neighborItems(neighbors []vectorstore.Neighbor) []vectorstore.Item {
	if len(neighbors) == 0 {
		return nil
	}
	out := make([]vectorstore.Item, len(neighbors))
	for i, n := range neighbors {
		out[i] = n.Item
	}
	return out
}

// canceledContext returns a context that is already canceled, for exercising
// cancellation paths.
func canceledContext(tb testing.TB) context.Context {
	tb.Helper()
	ctx, cancel := context.WithCancel(tb.Context())
	cancel()
	return ctx
}

// expiredContext returns a context whose deadline is already in the past, for
// exercising deadline-exceeded paths.
func expiredContext(tb testing.TB) context.Context {
	tb.Helper()
	ctx, cancel := context.WithDeadline(tb.Context(), time.Unix(0, 0))
	tb.Cleanup(cancel)
	return ctx
}
